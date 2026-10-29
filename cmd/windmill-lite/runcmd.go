package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/wl"
)

// runRun is `windmill-lite run <path> --bundle <b> --args <json|@file>`
// (ADR 0009): it opens the bundle with wl, runs the flow or script at
// path, and prints the outcome as JSON on stdout. It exits 0 when the run
// succeeded or was skipped, 1 when it failed or was canceled or could not
// start, and 2 on a usage error. SIGINT and SIGTERM cancel the run.
func runRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run", stderr)
	bundlePath := fs.String("bundle", "", "the `bundle` to run: a directory, or a .wlb archive")
	argText := fs.String("args", "{}", "the run's arguments: a JSON object, or @`file` holding one")
	kind := fs.String("kind", wl.KindFlow, "what path names: flow or script")
	id := fs.String("id", "", "the run's job `id` (default: a new UUID)")
	bun := fs.String("bun", envOr("WL_BUN", "bun"), "the Bun `executable`, which must be Bun 1.3.10 (default $WL_BUN, or bun)")
	runtime := fs.String("harness-runtime", os.Getenv("WL_HARNESS_RUNTIME"), "the Bun harness runtime `directory` the bundle was built for (default $WL_HARNESS_RUNTIME)")
	mode := fs.String("module-state", "", "the module-state `mode`, warm or fresh (default: the bundle's)")
	data := fs.String("data", "", "the data `directory` the runtime writes into (default: a temporary one, removed afterwards)")
	digest := fs.String("digest", "", "the bundle `digest` the bundle must have")
	variables := fs.String("variables", "", "a JSON `file` of host variables, keyed by path, as in a bundle's data/variables.json; secrets go here")
	resources := fs.String("resources", "", "a JSON `file` of host resources, keyed by path, as in a bundle's data/resources.json")
	trace := fs.Bool("trace", false, "print the run's steps and platform calls with the outcome")
	pos, ok := parse(fs, args)
	if !ok {
		return 2
	}
	if len(pos) != 1 || *bundlePath == "" {
		fmt.Fprintln(stderr, "usage: windmill-lite run <path> --bundle <bundle> [--args <json|@file>] [flags]")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "windmill-lite run: %v\n", err)
		return 1
	}
	input, err := argsOf(*argText)
	if err != nil {
		return fail(err)
	}
	opts := wl.Options{
		Bun:            *bun,
		HarnessRuntime: *runtime,
		DataDir:        *data,
		ModuleState:    *mode,
		Digest:         *digest,
		EngineVersion:  resolveVersion(),
		Diagnostics:    stderr,
	}
	if opts.Variables, err = hostVariables(*variables); err != nil {
		return fail(err)
	}
	if opts.Resources, err = hostResources(*resources); err != nil {
		return fail(err)
	}
	rt, err := wl.Open(*bundlePath, opts)
	if err != nil {
		return fail(err)
	}
	defer rt.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := rt.Run(ctx, pos[0], input, wl.RunOptions{Kind: *kind, ID: *id})
	if err != nil {
		return fail(err)
	}
	out := runOutput{ID: res.ID, Status: res.Status, Result: res.Result, MayHaveMutated: res.MayHaveMutated, Warnings: []runWarning{}}
	for _, w := range res.Warnings {
		out.Warnings = append(out.Warnings, runWarning{Code: w.Code, Message: w.Message})
	}
	if *trace && res.Trace != nil {
		out.Trace = traceOf(res.Trace)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fail(err)
	}
	if res.Status == wl.StatusSuccess || res.Status == wl.StatusSkipped {
		return 0
	}
	return 1
}

// argsOf reads --args: a JSON object, or @file holding one.
func argsOf(text string) (json.RawMessage, error) {
	if name, ok := strings.CutPrefix(text, "@"); ok {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		text = string(data)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("--args is not JSON")
	}
	return json.RawMessage(text), nil
}

// hostVariables reads a variables file: {path: {value, is_secret,
// encrypted}}, an encrypted value being Windmill ciphertext (JE-43).
func hostVariables(name string) (map[string]wl.Variable, error) {
	if name == "" {
		return nil, nil
	}
	var in map[string]struct {
		Value     string `json:"value"`
		IsSecret  bool   `json:"is_secret"`
		Encrypted bool   `json:"encrypted"`
	}
	if err := readJSON(name, &in); err != nil {
		return nil, err
	}
	out := make(map[string]wl.Variable, len(in))
	for p, v := range in {
		out[p] = wl.Variable{Value: v.Value, Secret: v.IsSecret, Ciphertext: v.Encrypted}
	}
	return out, nil
}

// hostResources reads a resources file: {path: {resource_type, value}}.
func hostResources(name string) (map[string]wl.Resource, error) {
	if name == "" {
		return nil, nil
	}
	var in map[string]struct {
		ResourceType string          `json:"resource_type"`
		Value        json.RawMessage `json:"value"`
	}
	if err := readJSON(name, &in); err != nil {
		return nil, err
	}
	out := make(map[string]wl.Resource, len(in))
	for p, r := range in {
		out[p] = wl.Resource{Type: r.ResourceType, Value: r.Value}
	}
	return out, nil
}

func readJSON(name string, v any) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// runOutput is what `run` prints. Its trace is a summary for debugging, not
// the trace's wire form, which belongs to the adapters (F5).
type runOutput struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result"`
	MayHaveMutated bool            `json:"may_have_mutated"`
	Warnings       []runWarning    `json:"warnings"`
	Trace          *runTrace       `json:"trace,omitempty"`
}

type runWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type runTrace struct {
	ModuleState   string        `json:"module_state"`
	Steps         []runStep     `json:"steps"`
	PlatformCalls []runPlatform `json:"platform_calls"`
}

type runStep struct {
	Site       conformance.SitePath     `json:"site"`
	Attempt    int                      `json:"attempt,omitempty"`
	Superseded bool                     `json:"superseded,omitempty"`
	Job        string                   `json:"job,omitempty"`
	Status     conformance.StepStatus   `json:"status"`
	Module     conformance.ModuleStatus `json:"module"`
	Skipped    bool                     `json:"skipped,omitempty"`
	Result     *jsonval.Value           `json:"result,omitempty"`
}

type runPlatform struct {
	Site    conformance.SitePath `json:"site"`
	Job     string               `json:"job"`
	Method  string               `json:"method"`
	Path    string               `json:"path"`
	Route   string               `json:"route"`
	Status  int                  `json:"status"`
	Message string               `json:"message,omitempty"`
}

func traceOf(tr *wl.Trace) *runTrace {
	out := &runTrace{ModuleState: string(tr.ModuleState), Steps: []runStep{}, PlatformCalls: []runPlatform{}}
	for _, s := range tr.Steps {
		st := runStep{Site: s.Site, Attempt: s.Attempt, Superseded: s.Superseded, Job: s.Job, Status: s.Status, Module: s.Module, Skipped: s.Skipped}
		if !s.Result.IsZero() {
			st.Result = &s.Result
		}
		out.Steps = append(out.Steps, st)
	}
	for _, c := range tr.PlatformCalls {
		out.PlatformCalls = append(out.PlatformCalls, runPlatform{Site: c.Site, Job: c.Job, Method: c.Method, Path: c.Path, Route: c.Route, Status: c.Status, Message: c.Message})
	}
	return out
}
