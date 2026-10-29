package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Stub mode (ADR 0011): every script body of a fixture's sync tree is
// replaced by a stub that keeps main's signature and asks a run-scoped
// replay service for its outcome. The service locates the calling job's
// site in the run (on the oracle by walking the job tree, on windmill-lite
// from the run trace), counts the attempt, answers with the outcome the
// run's replay table gives for that site and attempt, and records the call
// as one of the run's effects.

// ReplayPath is where the replay service listens, below the effect sink's
// URL (WL_EFFECT_SINK, OR-16): a stub POSTs its call to
// $WL_EFFECT_SINK/replay.
const ReplayPath = "/replay"

// Outcome is what a replay table says one stubbed call does: return a
// result, or throw an error. Result and Error hold the table's JSON text,
// so the stub returns members in the table's order.
type Outcome struct {
	// Result is the value the stub returns; nil for an error outcome. A
	// JSON null result is the text "null".
	Result json.RawMessage
	// Error is the object the stub throws as an Error: its "message"
	// becomes the message, "name" (default "Error") the name, and every
	// other member an own property of the error, which the Bun wrapper
	// stores under "extra" (OR-12).
	Error json.RawMessage
}

// UnmarshalJSON reads {"result": <value>} or {"error": {"message": …,
// "name": …, …}}: exactly one of the two. An error has a string message,
// a string name if any, and no "stack" or "step_id", which Windmill writes
// itself (OR-12).
func (o *Outcome) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return errors.New(`an outcome is an object: {result: …} or {error: {message: …}}`)
	}
	res, hasRes := m["result"]
	e, hasErr := m["error"]
	if len(m) != 1 || hasRes == hasErr {
		return fmt.Errorf(`an outcome has exactly one member, result or error; got %s`, memberNames(m))
	}
	if hasRes {
		*o = Outcome{Result: append(json.RawMessage(nil), res...)}
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(e, &fields); err != nil || fields == nil {
		return errors.New("an error outcome is an object with a message")
	}
	var msg string
	if raw, ok := fields["message"]; !ok || json.Unmarshal(raw, &msg) != nil {
		return errors.New("an error outcome needs a string message")
	}
	if raw, ok := fields["name"]; ok {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			return errors.New("an error outcome's name must be a string")
		}
	}
	for _, k := range []string{"stack", "step_id"} {
		if _, ok := fields[k]; ok {
			return fmt.Errorf("an error outcome cannot set %s: Windmill writes it (OR-12)", k)
		}
	}
	*o = Outcome{Error: append(json.RawMessage(nil), e...)}
	return nil
}

// MarshalJSON writes the outcome as UnmarshalJSON reads it, which is also
// the replay service's answer to a stub.
func (o Outcome) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	if o.Error != nil {
		b.WriteString(`{"error":`)
		b.Write(o.Error)
	} else {
		b.WriteString(`{"result":`)
		if o.Result == nil {
			b.WriteString("null")
		} else {
			b.Write(o.Result)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func memberNames(m map[string]json.RawMessage) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, strconv.Quote(k))
	}
	sort.Strings(names)
	return fmt.Sprint(names)
}

// ReplayTable is a run's replay table (runs/<name>.yaml, key replay): for
// each module site, the outcome of each attempt, from attempt 0. A site
// is written as canonical site-path text (ADR 0011) and ends in a module
// or slot segment, since the attempt is the list index, not an r:
// segment; the root "" is the site of a script run. An outcome on its own
// stands for a list of one.
type ReplayTable map[SitePath][]Outcome

// UnmarshalJSON reads a table and checks its sites and outcomes.
func (t *ReplayTable) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return errors.New("replay is an object from site paths to outcomes")
	}
	out := make(ReplayTable, len(raw))
	for k, v := range raw {
		site, err := ParseSitePath(k)
		if err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		// The root is the site of a script run's own job.
		if segs := site.Segments(); len(segs) > 0 && segs[len(segs)-1].Kind() != KindModule && segs[len(segs)-1].Kind() != KindSlot {
			return fmt.Errorf("replay: site %q is not a module: a stub runs as a module's job, and its attempt is the outcome's index", k)
		}
		var list []Outcome
		if t := bytes.TrimLeft(v, " \t\r\n"); len(t) > 0 && t[0] == '[' {
			if err := json.Unmarshal(v, &list); err != nil {
				return fmt.Errorf("replay %s: %w", k, err)
			}
		} else {
			var one Outcome
			if err := json.Unmarshal(v, &one); err != nil {
				return fmt.Errorf("replay %s: %w", k, err)
			}
			list = []Outcome{one}
		}
		if len(list) == 0 {
			return fmt.Errorf("replay %s: no outcomes", k)
		}
		out[site] = list
	}
	*t = out
	return nil
}

// Lookup returns the outcome of attempt n at site.
func (t ReplayTable) Lookup(site SitePath, n int) (Outcome, bool) {
	list := t[site]
	if n < 0 || n >= len(list) {
		return Outcome{}, false
	}
	return list[n], true
}

// Locator resolves the site of a stub's job inside a run while the run is
// in progress: on the oracle by walking the job tree from the root, on
// windmill-lite from the run trace. The site is the module's, never an r:
// attempt segment; the service counts attempts itself.
type Locator interface {
	Locate(ctx context.Context, root, job string) (SitePath, error)
}

// ReplayRequest is the body a stub POSTs: the job identities of OR-29 as
// the step sees them, and the arguments main received, in parameter
// order. Absent environment values are null.
type ReplayRequest struct {
	Root     string          `json:"root"`
	RootFlow json.RawMessage `json:"root_flow"`
	Flow     json.RawMessage `json:"flow"`
	Job      string          `json:"job"`
	Step     json.RawMessage `json:"step"`
	Args     json.RawMessage `json:"args"`
}

// ReplayCall is one answered stub call of a run.
type ReplayCall struct {
	// Job is the calling job (WM_JOB_ID).
	Job string
	// Site and Attempt are where the call was resolved: the module's site
	// and the number of distinct job ids already seen at that site in the
	// run (ADR 0011). They are unset when Err is set.
	Site    SitePath
	Attempt int
	// Outcome is the table's answer; nil when the table has none (a miss)
	// or the call could not be resolved.
	Outcome *Outcome
	// Effect is the call as the run's effect: site, attempt and the
	// request's members (ReplayEffect).
	Effect json.RawMessage
	// Duplicates counts repeated calls of the same job, answered with the
	// same outcome (outcomes are idempotent per raw job id).
	Duplicates int
	// Err is set when the call was refused: replay_miss or
	// replay_unresolved, as the JSON the stub received.
	Err string

	code int // the HTTP status of a refusal
}

// ReplayEffect is the effect a call records, with members in this order:
// the resolved site and attempt, then the step id and the job, flow, root
// flow and root ids the stub sent, then the arguments.
func ReplayEffect(site SitePath, attempt int, req ReplayRequest) json.RawMessage {
	var b bytes.Buffer
	b.WriteString(`{"site":`)
	writeJSONString(&b, site.String())
	fmt.Fprintf(&b, `,"attempt":%d,"step":`, attempt)
	writeRaw(&b, req.Step)
	b.WriteString(`,"job":`)
	writeJSONString(&b, req.Job)
	b.WriteString(`,"flow":`)
	writeRaw(&b, req.Flow)
	b.WriteString(`,"root_flow":`)
	writeRaw(&b, req.RootFlow)
	b.WriteString(`,"root":`)
	writeJSONString(&b, req.Root)
	b.WriteString(`,"args":`)
	writeRaw(&b, req.Args)
	b.WriteByte('}')
	return json.RawMessage(b.Bytes())
}

func writeRaw(b *bytes.Buffer, v json.RawMessage) {
	if len(bytes.TrimSpace(v)) == 0 {
		b.WriteString("null")
		return
	}
	var c bytes.Buffer
	if json.Compact(&c, v) != nil {
		b.WriteString("null")
		return
	}
	b.Write(c.Bytes())
}

// ReplayService is the run-scoped replay service: an HTTP handler that
// answers stub calls of the runs that are open, each with its own table
// and locator. It is safe for concurrent use, so concurrent outer runs and
// parallel iterations are answered at once.
type ReplayService struct {
	// LocateTimeout bounds the resolution of one call; zero means 60 s.
	LocateTimeout time.Duration

	mu      sync.Mutex
	runs    map[string]*replayRun
	foreign int
}

type replayRun struct {
	table   ReplayTable
	locator Locator
	// seen holds, per site, the distinct job ids already answered there,
	// in order: a job's attempt is its index.
	seen  map[SitePath][]string
	byJob map[string]*ReplayCall
	calls []*ReplayCall
}

// Open starts answering the stubs of the run whose root job id is root,
// with its table and a locator for its job tree.
func (s *ReplayService) Open(root string, table ReplayTable, loc Locator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = map[string]*replayRun{}
	}
	s.runs[root] = &replayRun{table: table, locator: loc, seen: map[SitePath][]string{}, byJob: map[string]*ReplayCall{}}
}

// Close stops answering the run and returns its calls in arrival order.
func (s *ReplayService) Close(root string) []ReplayCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[root]
	delete(s.runs, root)
	if r == nil {
		return nil
	}
	out := make([]ReplayCall, len(r.calls))
	for i, c := range r.calls {
		out[i] = *c
	}
	return out
}

// Foreign returns the number of calls refused because their root was no
// open run.
func (s *ReplayService) Foreign() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.foreign
}

// maxReplayRequest bounds one call's body.
const maxReplayRequest = 1 << 20

// ServeHTTP answers one stub call. 200 carries the outcome; anything else
// is a refusal, a JSON object with an "error" code, which the stub throws:
//
//	400 replay_bad_request   the body is not a call
//	409 replay_foreign_root  root is no open run (OR-16/c, OR-V6)
//	504 replay_unresolved    the job was not found in the run in time
//	404 replay_miss          the table has no outcome for the site and attempt
func (s *ReplayService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a stub call", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReplayRequest+1))
	var req ReplayRequest
	if err != nil || len(body) > maxReplayRequest || json.Unmarshal(body, &req) != nil || req.Root == "" || req.Job == "" {
		replyRefusal(w, http.StatusBadRequest, "replay_bad_request", "")
		return
	}
	s.mu.Lock()
	run := s.runs[req.Root]
	if run == nil {
		s.foreign++
		s.mu.Unlock()
		replyRefusal(w, http.StatusConflict, "replay_foreign_root", `,"root":`+jsonText(req.Root))
		return
	}
	if c := run.byJob[req.Job]; c != nil {
		c.Duplicates++
		s.mu.Unlock()
		reply(w, c)
		return
	}
	s.mu.Unlock()

	timeout := s.LocateTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	site, locErr := run.locator.Locate(ctx, req.Root, req.Job)
	cancel()

	s.mu.Lock()
	if c := run.byJob[req.Job]; c != nil {
		// A concurrent call of the same job was answered first.
		c.Duplicates++
		s.mu.Unlock()
		reply(w, c)
		return
	}
	c := &ReplayCall{Job: req.Job}
	if locErr != nil {
		c.code = http.StatusGatewayTimeout
		c.Err = `{"error":"replay_unresolved","job":` + jsonText(req.Job) + `,"reason":` + jsonText(locErr.Error()) + `}`
	} else {
		c.Site, c.Attempt = site, len(run.seen[site])
		run.seen[site] = append(run.seen[site], req.Job)
		c.Effect = ReplayEffect(site, c.Attempt, req)
		if o, ok := run.table.Lookup(site, c.Attempt); ok {
			c.Outcome = &o
		} else {
			c.code = http.StatusNotFound
			c.Err = `{"error":"replay_miss","site":` + jsonText(site.String()) + `,"attempt":` + strconv.Itoa(c.Attempt) + `}`
		}
	}
	run.byJob[req.Job] = c
	run.calls = append(run.calls, c)
	s.mu.Unlock()
	reply(w, c)
}

// CheckReplay holds a stub run's answered calls against where the run's
// observation puts each job: walked maps every job id it reached to its
// site, a failed attempt k at <module>/r:<k> (on the oracle from the walk,
// on windmill-lite from the run trace), and scripts are the ids of the
// jobs that executed a script. Both sides apply the same rules:
//
//   - every call was answered: a call the table had no outcome for
//     (replay_miss) or whose job was not found in time (replay_unresolved)
//     fails the run, since the fixture's table or tree is wrong;
//   - every call was resolved where the observation puts its job: a failed
//     attempt k at <site>/r:<k>, the module's last job at <site> itself
//     with k equal to the number of failed attempts before it
//     (replay_site_mismatch);
//   - every job that executed a script called the service, so no original
//     body ran (stub_not_called).
//
// The error's text is the refusal as one JSON object.
func CheckReplay(calls []ReplayCall, walked map[string]SitePath, scripts []string) error {
	called := map[string]bool{}
	attempts := map[SitePath]bool{}
	for _, site := range walked {
		if segs := site.Segments(); len(segs) > 0 && segs[len(segs)-1].Kind() == KindAttempt {
			attempts[site] = true
		}
	}
	for _, c := range calls {
		if c.Err != "" {
			return errors.New(c.Err)
		}
		called[c.Job] = true
		at, ok := walked[c.Job]
		failed := c.Site.Child(Attempt(c.Attempt))
		last := at == c.Site && !attempts[failed] && (c.Attempt == 0 || attempts[c.Site.Child(Attempt(c.Attempt-1))])
		if !ok || at != failed && !last {
			got := "(not walked)"
			if ok {
				got = at.String()
			}
			return fmt.Errorf(`{"error":"replay_site_mismatch","job":%s,"resolved":%s,"attempt":%d,"walked":%s}`,
				jsonText(c.Job), jsonText(c.Site.String()), c.Attempt, jsonText(got))
		}
	}
	var missing []string
	for _, id := range scripts {
		if !called[id] {
			missing = append(missing, walked[id].String())
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		var b bytes.Buffer
		b.WriteString(`{"error":"stub_not_called","sites":[`)
		for i, s := range missing {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(&b, s)
		}
		b.WriteString("]}")
		return errors.New(b.String())
	}
	return nil
}

func reply(w http.ResponseWriter, c *ReplayCall) {
	w.Header().Set("Content-Type", "application/json")
	if c.Outcome != nil {
		data, _ := c.Outcome.MarshalJSON()
		_, _ = w.Write(data)
		return
	}
	w.WriteHeader(c.code)
	_, _ = io.WriteString(w, c.Err)
}

func replyRefusal(w http.ResponseWriter, code int, refusal, extra string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `{"error":"`+refusal+`"`+extra+`}`)
}

func jsonText(s string) string {
	var b bytes.Buffer
	writeJSONString(&b, s)
	return b.String()
}
