package wl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// hookCase is one case of conformance/hooks: a synthetic flow, the host's
// signals, and what the run must show (see the files' header).
type hookCase struct {
	Name string          `json:"name"`
	Spec []string        `json:"spec"`
	Flow json.RawMessage `json:"flow"`
	Args json.RawMessage `json:"args"`
	Host struct {
		Fail             map[string]json.RawMessage `json:"fail"`
		Value            map[string]json.RawMessage `json:"value"`
		LatchAtStart     []string                   `json:"latch_at_start"`
		LatchAtEnd       []string                   `json:"latch_at_end"`
		CancelAtStart    []string                   `json:"cancel_at_start"`
		CancelAtEnd      []string                   `json:"cancel_at_end"`
		Canceled         bool                       `json:"canceled"`
		FatalErrorNames  []string                   `json:"fatal_error_names"`
		HaltBefore       []string                   `json:"halt_before"`
		HaltAfter        []string                   `json:"halt_after"`
		Clock            string                     `json:"clock"`
		PreWriteDeadline int                        `json:"pre_write_deadline_ms"`
	} `json:"host"`
	Expect struct {
		Status     string          `json:"status"`
		Latched    string          `json:"latched"`
		Ran        []string        `json:"ran"`
		NotRan     []string        `json:"not_ran"`
		Result     json.RawMessage `json:"result"`
		Deviations []string        `json:"deviations"`
	} `json:"expect"`
}

const hookCaseFlow = "f/hooks/case"

// errHostLatch is the error a case's hooks latch (H5).
var errHostLatch = errors.New("host latch")

// TestHookCases runs conformance/hooks (milestone F2b's latch-precedence
// cases) through wl: each case's flow, as a bundle of its own, with the
// host signals it names as public hooks and run options.
func TestHookCases(t *testing.T) {
	files, err := filepath.Glob("../conformance/hooks/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no hook case files (%v)", err)
	}
	n := 0
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text, err := yamlx.ToJSON(src)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var doc struct {
			Cases []hookCase `json:"cases"`
		}
		if err := json.Unmarshal(text, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, c := range doc.Cases {
			n++
			t.Run(filepath.Base(file)+"/"+c.Name, func(t *testing.T) {
				if len(c.Spec) == 0 {
					t.Fatal("the case cites no spec ID")
				}
				runHookCase(t, c)
			})
		}
	}
	if n == 0 {
		t.Fatal("no hook cases")
	}
}

func runHookCase(t *testing.T, c hookCase) {
	t.Helper()
	dir := writeBundleOf(t, map[string]string{hookCaseFlow: string(c.Flow)}, nil, Version(), nil, nil)
	rt, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := &Cancellation{Reason: "host stop", Canceler: "host"}
	if c.Host.Canceled {
		cancel(stop)
	}
	sels := func(ids []string) []Selector {
		var out []Selector
		for _, id := range ids {
			out = append(out, module(hookCaseFlow, id))
		}
		return out
	}
	h := &Hooks{FatalErrorNames: c.Host.FatalErrorNames, HaltBefore: sels(c.Host.HaltBefore), HaltAfter: sels(c.Host.HaltAfter)}
	for id, v := range c.Host.Fail {
		h.Intercept = append(h.Intercept, Interceptor{Selector: module(hookCaseFlow, id), Func: func(context.Context, Call) (Outcome, bool, error) {
			return Outcome{Result: v, Failed: true}, true, nil
		}})
	}
	for id, v := range c.Host.Value {
		h.Intercept = append(h.Intercept, Interceptor{Selector: module(hookCaseFlow, id), Static: v})
	}
	at := func(latch, stopAt []string, s Site, r RunHandle) {
		if slices.Contains(stopAt, s.Module) {
			cancel(stop)
		}
		if slices.Contains(latch, s.Module) {
			r.Latch(errHostLatch)
		}
	}
	h.OnModuleStart = func(_ context.Context, r RunHandle, s Site) error {
		at(c.Host.LatchAtStart, c.Host.CancelAtStart, s, r)
		return nil
	}
	h.OnModuleEnd = func(_ context.Context, r RunHandle, s Site, _ StepRecord) error {
		at(c.Host.LatchAtEnd, c.Host.CancelAtEnd, s, r)
		return nil
	}
	opts := RunOptions{Hooks: h}
	var clock *RecordClock
	if c.Host.Clock == "record" {
		clock = &RecordClock{}
		opts.Clock = clock
	}
	if c.Host.PreWriteDeadline > 0 {
		opts.PreWriteDeadline = time.Now().Add(time.Duration(c.Host.PreWriteDeadline) * time.Millisecond)
	}
	started := time.Now()
	res, err := rt.Run(ctx, hookCaseFlow, c.Args, opts)
	if err != nil {
		t.Fatal(err)
	}
	e := c.Expect
	if res.Status != e.Status {
		t.Errorf("status %s, want %s (result %s, latched %v)", res.Status, e.Status, res.Result, res.Latched)
	}
	var fatal *FatalError
	switch e.Latched {
	case "":
		if res.Latched != nil {
			t.Errorf("latched %v", res.Latched)
		}
	case "host":
		if !errors.Is(res.Latched, errHostLatch) {
			t.Errorf("latched %v, want the host's latch", res.Latched)
		}
	case "fatal":
		if !errors.As(res.Latched, &fatal) || !slices.Contains(c.Host.FatalErrorNames, fatal.Name) {
			t.Errorf("latched %v, want a FatalError", res.Latched)
		}
	default:
		t.Fatalf("unknown latched %q", e.Latched)
	}
	for _, p := range e.Ran {
		if _, ok := res.Trace.Step(mustSite(t, p)); !ok {
			t.Errorf("no record of %s", p)
		}
	}
	for _, p := range e.NotRan {
		if rec, ok := res.Trace.Step(mustSite(t, p)); ok && rec.Ran {
			t.Errorf("%s ran (%s)", p, rec.Status)
		}
	}
	if e.Result != nil && !sameJSON(res.Result, e.Result) {
		t.Errorf("result %s, want %s", res.Result, e.Result)
	}
	if !slices.Equal(res.Trace.Deviations, e.Deviations) && !(len(res.Trace.Deviations) == 0 && len(e.Deviations) == 0) {
		t.Errorf("deviations %v, want %v", res.Trace.Deviations, e.Deviations)
	}
	if clock != nil && time.Since(started) > 10*time.Second {
		t.Errorf("a record-clock run took %s", time.Since(started))
	}
}

func mustSite(t *testing.T, p string) SitePath {
	t.Helper()
	s, err := conformance.ParseSitePath(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sameJSON compares two JSON texts as values.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}
