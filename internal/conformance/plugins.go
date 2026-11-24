package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The plugins level: conformance/plugins/hooks.yaml holds the host-plugin
// cases. A case is one synthetic flow, a declarative host plugin that
// answers its hooks, and one or more runs of the flow, each with its own
// hooks, budgets and expected outcome. Every case runs through a TypeScript
// plugin in warm harness processes and in host mode: its callbacks over
// host_call, and its around wrappers around each invocation, in the process
// that runs it. Unless the case sets go: false, it also runs through Go
// callbacks built from the same plugin, with the same expectations. A case
// with a pool of its own runs only through the TypeScript plugin in warm
// harness processes.

// PluginFile is a plugin case file.
type PluginFile struct {
	// TimeoutMS is the plugin timeout of the runtimes that run the cases.
	TimeoutMS int `json:"timeout_ms"`
	// Scripts are the folder's scripts, by their path in the sync folder.
	Scripts map[string]string `json:"scripts"`
	Cases   []PluginCase      `json:"cases"`
}

// PluginCase is one plugin case.
type PluginCase struct {
	ID    string   `json:"id"`
	Spec  []string `json:"spec"`
	Title string   `json:"title"`
	// Flow is the flow's value, at FlowPath.
	Flow json.RawMessage `json:"flow"`
	// Plugin is the case's declarative plugin, as the TypeScript case
	// plugin reads it; Hooks is the same, decoded.
	Plugin   json.RawMessage `json:"plugin"`
	Hooks    PluginSpec      `json:"-"`
	Provided []struct {
		Module   string `json:"module"`
		Provider string `json:"provider"`
	} `json:"provided"`
	// Run is the run's own hooks and budgets, which a Go host gives in
	// RunOptions and a TypeScript host in its run frame.
	Run    PluginRun    `json:"run"`
	Expect PluginExpect `json:"expect"`
	// Then are more runs of the case's flow, in order after its run, on
	// the same runtime, and in host mode on the same connection, each
	// with its own hooks and budgets and its own expectations.
	Then []PluginThen `json:"then"`
	// Go, when false, runs the case only through the TypeScript plugin.
	Go *bool `json:"go"`
	// Pool, when set, runs the case in a warm runtime of its own with these
	// pool settings, and only there: host mode has one process, and Go
	// callbacks keep no state in a harness process, so it needs go: false.
	Pool *PluginPool `json:"pool"`
}

// PluginPool is the pool of a case's own warm runtime (wl.PoolOptions): its
// maximum, its lag threshold, and whether every run is pinned.
type PluginPool struct {
	Max            int  `json:"max"`
	LagThresholdMS int  `json:"lag_threshold_ms"`
	PinRuns        bool `json:"pin_runs"`
}

// PluginSpec is a declarative plugin (the file's header says what each
// field does).
type PluginSpec struct {
	Intercept []struct {
		Module  string          `json:"module"`
		Result  json.RawMessage `json:"result"`
		Failed  json.RawMessage `json:"failed"`
		Static  json.RawMessage `json:"static"`
		Decline bool            `json:"decline"`
		Throw   string          `json:"throw"`
	} `json:"intercept"`
	RewriteArgs []struct {
		Module string          `json:"module"`
		Args   json.RawMessage `json:"args"`
	} `json:"rewrite_args"`
	HaltBefore []string `json:"halt_before"`
	HaltAfter  []string `json:"halt_after"`
	// WriteCapable are module ids (H3); FirstWrite is what
	// beforeFirstWrite does: "approve", or "veto: <reason>".
	WriteCapable    []string `json:"write_capable"`
	FirstWrite      string   `json:"first_write"`
	FatalErrorNames []string `json:"fatal_error_names"`
	Around          []struct {
		Module string `json:"module"`
		Throw  string `json:"throw"`
	} `json:"around"`
	Observe   bool              `json:"observe"`
	Env       map[string]string `json:"env"`
	Variables map[string]string `json:"variables"`
	// FailVariables and FailResources make H13's provider fail for a path:
	// throw, or answer after DelayMS, beyond the plugin timeout. SlowStartMS
	// delays each onModuleStart.
	FailVariables map[string]PluginFailure `json:"fail_variables"`
	FailResources map[string]PluginFailure `json:"fail_resources"`
	SlowStartMS   int                      `json:"slow_start_ms"`
	Providers     map[string]struct {
		Result  json.RawMessage `json:"result"`
		Failed  json.RawMessage `json:"failed"`
		Decline bool            `json:"decline"`
		Throw   string          `json:"throw"`
		DelayMS int             `json:"delay_ms"`
	} `json:"providers"`
	Warnings []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"warnings"`
	// State keeps a count per run in the plugin, in the process that
	// answers: each onModuleStart raises it and logs state:<site>:<count>.
	State bool `json:"state"`
}

// PluginFailure is how an H13 provider of a case plugin fails for a path.
type PluginFailure struct {
	Throw   string `json:"throw"`
	DelayMS int    `json:"delay_ms"`
}

// PluginThen is one of a case's later runs.
type PluginThen struct {
	Run    PluginRun    `json:"run"`
	Expect PluginExpect `json:"expect"`
}

// PluginRun is a case run's own hooks and budgets (the file's header says
// what each field does).
type PluginRun struct {
	WriteCapable       []string `json:"write_capable"`
	HaltBefore         []string `json:"halt_before"`
	HaltAfter          []string `json:"halt_after"`
	FatalErrorNames    []string `json:"fatal_error_names"`
	DeadlineMS         *int     `json:"deadline_ms"`
	PreWriteDeadlineMS *int     `json:"pre_write_deadline_ms"`
	Clock              string   `json:"clock"`
}

// PluginExpect is what a case expects of its run.
type PluginExpect struct {
	Status         string          `json:"status"`
	MayHaveMutated *bool           `json:"may_have_mutated"`
	Result         json.RawMessage `json:"result"`
	Latched        string          `json:"latched"`
	Log            []string        `json:"log"`
	Warnings       []string        `json:"warnings"`
	NotRan         []string        `json:"not_ran"`
	Deviations     []string        `json:"deviations"`
	CancelCause    string          `json:"cancel_cause"`
}

// PluginLog is the code of the warning in which a case plugin's onRunEnd
// reports its log of the run, as a JSON array of strings.
const PluginLog = "plugin_log"

// PluginTrace is the code of the warning in which the TypeScript case
// plugin's onRunEnd reports what the run's trace showed it, as a JSON
// object: ran, the sites whose job ran, and cancel_cause, the run's cause
// of cancellation, "" for none.
const PluginTrace = "plugin_trace"

// ReadSettlement reads a case plugin's report file: the runs its
// onRunSettled heard of, and those of them that settled while one of their
// hooks still ran (early). A missing file reports none.
func ReadSettlement(path string) (settled, early []string, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r struct {
			Run   string `json:"run"`
			Early bool   `json:"early"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		settled = append(settled, r.Run)
		if r.Early {
			early = append(early, r.Run)
		}
	}
	return settled, early, nil
}

// LoadPluginFile reads and checks a plugin case file.
func LoadPluginFile(path string) (*PluginFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	j, err := YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var f PluginFile
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i := range f.Cases {
		c := &f.Cases[i]
		if c.ID == "" || seen[c.ID] || strings.ContainsAny(c.ID, "/ ") {
			return nil, fmt.Errorf("%s: case %d: a unique id without slashes or spaces", path, i)
		}
		seen[c.ID] = true
		pd := json.NewDecoder(bytes.NewReader(c.Plugin))
		pd.DisallowUnknownFields()
		if err := pd.Decode(&c.Hooks); err != nil {
			return nil, fmt.Errorf("%s: case %s: plugin: %w", path, c.ID, err)
		}
		runs := append([]PluginThen{{Run: c.Run, Expect: c.Expect}}, c.Then...)
		for j, r := range runs {
			where := "run"
			if j > 0 {
				where = fmt.Sprintf("then[%d]", j-1)
			}
			if r.Expect.Status == "" {
				return nil, fmt.Errorf("%s: case %s: %s: expect.status is required", path, c.ID, where)
			}
			if r := r.Run; r.Clock != "" && r.Clock != "real" && r.Clock != "record" || negative(r.DeadlineMS) || negative(r.PreWriteDeadlineMS) {
				return nil, fmt.Errorf("%s: case %s: %s: a clock that is real or record, and deadlines that are not negative", path, c.ID, where)
			}
		}
		// Go callbacks cannot fail as a plugin's host_call does.
		if h := c.Hooks; (len(h.FailVariables) > 0 || len(h.FailResources) > 0 || h.SlowStartMS > 0) && c.RunsGo() {
			return nil, fmt.Errorf("%s: case %s: a plugin that fails or is slow runs only through TypeScript (go: false)", path, c.ID)
		}
		// Go callbacks keep no state in a harness process.
		if (c.Hooks.State || c.Pool != nil) && c.RunsGo() {
			return nil, fmt.Errorf("%s: case %s: a plugin that keeps state, or a case with a pool of its own, runs only through TypeScript (go: false)", path, c.ID)
		}
		if p := c.Pool; p != nil && (p.Max < 1 || p.LagThresholdMS < 0) {
			return nil, fmt.Errorf("%s: case %s: a pool's max is at least 1 and its lag threshold is not negative", path, c.ID)
		}
	}
	if len(f.Cases) == 0 {
		return nil, errors.New(path + ": no cases")
	}
	return &f, nil
}

// negative reports whether an optional number is present and below 0.
func negative(n *int) bool { return n != nil && *n < 0 }

// FlowPath is the path of the case's flow in the folder.
func (c PluginCase) FlowPath() string { return "f/plugins/" + c.ID }

// RunsGo reports whether the case runs through Go callbacks too.
func (c PluginCase) RunsGo() bool { return c.Go == nil || *c.Go }

// RunsHost reports whether the case runs in host mode too: a case with a
// pool of its own does not, since a host is one process.
func (c PluginCase) RunsHost() bool { return c.Pool == nil }

// WriteFolder writes the file's sync folder below dir: wmill.yaml, the
// scripts and each case's flow.
func (f *PluginFile) WriteFolder(dir string) error {
	files := map[string][]byte{"wmill.yaml": []byte("defaultTs: bun\n")}
	for p, src := range f.Scripts {
		files[p] = []byte(src)
	}
	for _, c := range f.Cases {
		flow, err := json.Marshal(map[string]any{"summary": c.Title, "value": c.Flow})
		if err != nil {
			return err
		}
		files[c.FlowPath()+".flow/flow.yaml"] = flow
	}
	for p, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Contract is the host contract of the file's bundle (JSON, which the
// contract's YAML reads): every case's flow a root, and the cases'
// provided sites, mandatory, named by their providers.
func (f *PluginFile) Contract() []byte {
	var roots, provided []map[string]any
	for _, c := range f.Cases {
		roots = append(roots, map[string]any{"flow": c.FlowPath()})
		for _, p := range c.Provided {
			provided = append(provided, map[string]any{"selector": map[string]any{"flow": c.FlowPath(), "module": p.Module}, "provider": p.Provider, "mandatory": true})
		}
	}
	b, _ := json.Marshal(map[string]any{"roots": roots, "provided": provided})
	return b
}

// PluginOutcome is what a case's run gave: its status, result (stored),
// latched error, warnings and the sites that ran a job.
type PluginOutcome struct {
	Status         string
	MayHaveMutated bool
	Result         json.RawMessage
	Latched        string
	Warnings       []struct{ Code, Message string }
	// Ran holds, for each site the trace has a record of, whether a job
	// ran for it; nil for a runner without the run's trace, whose plugin
	// must then report the sites in its PluginTrace warning.
	Ran map[string]bool
	// Deviations are the run's run-time deviations (H7, H10).
	Deviations []string
	// CancelCause is why a canceled run was canceled, "" otherwise.
	CancelCause string
}

// Check compares the outcome of the case's run with its expectations and
// returns the differences, none when it matches.
func (c PluginCase) Check(o PluginOutcome) []string { return c.Expect.Check(o) }

// Check compares a run's outcome with the expectations and returns the
// differences, none when it matches.
func (e PluginExpect) Check(o PluginOutcome) []string {
	var diffs []string
	if o.Status != e.Status {
		diffs = append(diffs, fmt.Sprintf("status %s, want %s", o.Status, e.Status))
	}
	if e.MayHaveMutated != nil && o.MayHaveMutated != *e.MayHaveMutated {
		diffs = append(diffs, fmt.Sprintf("may_have_mutated %v, want %v", o.MayHaveMutated, *e.MayHaveMutated))
	}
	if e.Result != nil {
		if ok, _ := jsonEqualText(o.Result, e.Result); !ok {
			diffs = append(diffs, fmt.Sprintf("result %s, want %s", o.Result, e.Result))
		}
	}
	if e.Latched != "" && !strings.Contains(o.Latched, e.Latched) {
		diffs = append(diffs, fmt.Sprintf("latched %q, want one that contains %q", o.Latched, e.Latched))
	}
	if e.Latched == "" && o.Latched != "" {
		diffs = append(diffs, fmt.Sprintf("latched %q, want none", o.Latched))
	}
	var log []string
	var codes []string
	ran := o.Ran
	for _, w := range o.Warnings {
		switch w.Code {
		case PluginLog:
			if err := json.Unmarshal([]byte(w.Message), &log); err != nil {
				diffs = append(diffs, "the plugin's log is not a JSON array of strings: "+w.Message)
			}
			continue
		case PluginTrace:
			// The plugin's onRunEnd got the run's cause; and a runner
			// without the run's trace, in host mode, takes the sites that
			// ran from the plugin's. Both fields are required: an empty
			// array of sites is a report, a missing one is not.
			var seen struct {
				Ran         *[]string `json:"ran"`
				CancelCause *string   `json:"cancel_cause"`
			}
			if err := json.Unmarshal([]byte(w.Message), &seen); err != nil || seen.Ran == nil || seen.CancelCause == nil {
				diffs = append(diffs, "the plugin's trace report is not an object of ran, an array, and cancel_cause, a string: "+w.Message)
				continue
			}
			if *seen.CancelCause != o.CancelCause {
				diffs = append(diffs, fmt.Sprintf("the plugin's onRunEnd saw cancel_cause %q, the run has %q", *seen.CancelCause, o.CancelCause))
			}
			if o.Ran == nil {
				ran = map[string]bool{}
				for _, s := range *seen.Ran {
					ran[s] = true
				}
			}
			continue
		}
		codes = append(codes, w.Code)
	}
	if ran == nil {
		// A runner without the run's trace relies on the plugin's report.
		diffs = append(diffs, "no record of the sites that ran: the runner has no trace, and the plugin sent no "+PluginTrace)
	}
	if e.Log != nil && !slices.Equal(log, e.Log) {
		diffs = append(diffs, fmt.Sprintf("log %q, want %q", log, e.Log))
	}
	for _, w := range e.Warnings {
		if !slices.Contains(codes, w) {
			diffs = append(diffs, fmt.Sprintf("warnings %q, want one with code %q", codes, w))
		}
	}
	for _, s := range e.NotRan {
		if ran[s] {
			diffs = append(diffs, fmt.Sprintf("site %s ran a job, want none", s))
		}
	}
	if e.Deviations != nil && !slices.Equal(o.Deviations, e.Deviations) {
		diffs = append(diffs, fmt.Sprintf("deviations %q, want %q", o.Deviations, e.Deviations))
	}
	if e.CancelCause != "" && o.CancelCause != e.CancelCause {
		diffs = append(diffs, fmt.Sprintf("cancel_cause %q, want %q", o.CancelCause, e.CancelCause))
	}
	if o.Status != "canceled" && o.CancelCause != "" {
		diffs = append(diffs, fmt.Sprintf("cancel_cause %q of a run that is not canceled, want none", o.CancelCause))
	}
	return diffs
}

// jsonEqualText reports whether two JSON texts hold equal values.
func jsonEqualText(a, b json.RawMessage) (bool, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb), nil
}
