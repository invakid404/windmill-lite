package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestNormaliseCases runs testdata/normalise/cases.json.
func TestNormaliseCases(t *testing.T) {
	data, err := os.ReadFile("testdata/normalise/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		About string `json:"about"`
		IDs   map[string]struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"ids"`
		Cases []struct {
			caseMeta
			RuntimeErrors []json.RawMessage `json:"runtime_errors"`
			Raw           json.RawMessage   `json:"raw"`
			Want          json.RawMessage   `json:"want"`
		} `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	ids := map[string]SitePath{}
	var pairs []string
	for name, v := range file.IDs {
		p, err := ParseSitePath(v.Path)
		if err != nil {
			t.Fatal(err)
		}
		ids[v.ID] = p
		pairs = append(pairs, "${"+name+"}", v.ID)
	}
	r := strings.NewReplacer(pairs...)
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Spec) == 0 || !validProvenances[c.Provenance] {
				t.Fatalf("case needs spec IDs and a provenance (%q)", c.Provenance)
			}
			n := &Normaliser{IDs: ids}
			for _, e := range c.RuntimeErrors {
				n.RuntimeErrors = append(n.RuntimeErrors, json.RawMessage(r.Replace(string(e))))
			}
			got, err := n.Value(json.RawMessage(r.Replace(string(c.Raw))))
			if err != nil {
				t.Fatal(err)
			}
			g, err := IndentJSON(got)
			if err != nil {
				t.Fatal(err)
			}
			w, err := IndentJSON(c.Want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(g, w) {
				t.Fatalf("got\n%s\nwant\n%s", g, w)
			}
		})
	}
}

// TestMaskPaths checks fixture-declared stack and mask paths (OR-V3 clause
// c, OR-14 item 8, OR-29).
func TestMaskPaths(t *testing.T) {
	var o Observation
	if err := json.Unmarshal([]byte(`{"status":"success","result":{"env":{"WM_WORKSPACE":"wl-1a2b3c-1","WM_BASE_URL":"http://localhost:8000"},"trace":"at x"},"steps":{"m:a":{"status":"success","args":{},"result":{"base_url":"http://localhost:8000","n":1}}}}`), &o); err != nil {
		t.Fatal(err)
	}
	got, err := MaskPaths(o, []string{"/result/trace", "/result/env"}, []string{"/result/env/WM_WORKSPACE", "/steps/m:a/result/base_url", "/steps/m:b/result"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	g, err := IndentJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	w, err := IndentJSON([]byte(`{"status":"success","result":{"env":{"WM_WORKSPACE":"<masked>","WM_BASE_URL":"http://localhost:8000"},"trace":"<stack>"},"steps":{"m:a":{"status":"success","args":{},"result":{"base_url":"<masked>","n":1}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, w) {
		t.Fatalf("got\n%s\nwant\n%s", g, w)
	}
}

// TestStripLocations is the location-suffix rule of ADR 0011: a generated
// Internal error whose oracle text ends in a Rust location compares equal
// to windmill-lite's text, which has none (D14), while two user errors that
// differ only in such a suffix still differ.
func TestStripLocations(t *testing.T) {
	// spec: OR-14, OR-27
	const job = "0190a0c4-8c11-7b1e-9f3a-0123456789ab"
	ids := map[string]SitePath{job: SitePath{}.Child(Module("a"))}
	internal := func(loc string) string {
		return `{"error":{"name":"InternalErr","message":"Internal: Could not add completed job ` + job +
			`: error returned from database: unsupported Unicode escape sequence` + loc + `"}}`
	}
	user := func(loc string) string {
		return `{"error":{"name":"Error","message":"boom` + loc + `","stack":"s","step_id":"a"}}`
	}
	observe := func(t *testing.T, result string) Observation {
		t.Helper()
		n := &Normaliser{IDs: ids}
		v, err := n.Value(json.RawMessage(result))
		if err != nil {
			t.Fatal(err)
		}
		return Observation{Status: RunFailure, Result: v, Steps: map[SitePath]StepObservation{
			ids[job]: {Status: StepFailure, Args: json.RawMessage(`{}`), Result: v},
		}}
	}
	differences := func(t *testing.T, got, oracle Observation, at []string) []Difference {
		t.Helper()
		stripped, err := StripLocations(oracle, at)
		if err != nil {
			t.Fatal(err)
		}
		diffs, err := Compare(got, stripped, CompareOptions{WithRecording: true})
		if err != nil {
			t.Fatal(err)
		}
		return diffs
	}
	generated := []string{"/result", "/steps/m:a/result"}

	t.Run("generated internal error", func(t *testing.T) {
		got, oracle := observe(t, internal("")), observe(t, internal(" @windmill-worker/src/result_processor.rs:412:17"))
		if !strings.HasSuffix(string(oracle.Result), ` sequence <loc>"}}`) {
			t.Fatalf("the normaliser did not mask the location: %s", oracle.Result)
		}
		if d := differences(t, got, oracle, generated); len(d) != 0 {
			t.Fatalf("differences after stripping: %v", d)
		}
		// Without the rule, the two still differ: the rule is what equates
		// them.
		if d := differences(t, got, oracle, nil); len(d) != 2 {
			t.Fatalf("differences without stripping: %v", d)
		}
		// Only the pointers given are stripped.
		if d := differences(t, got, oracle, []string{"/result"}); len(d) != 1 || d[0].Pointer != "/steps/m:a/result/error/message" {
			t.Fatalf("differences stripping /result only: %v", d)
		}
	})
	t.Run("user errors", func(t *testing.T) {
		got, oracle := observe(t, user(" @a.rs:1:2")), observe(t, user(" @b.rs:3:4"))
		if d := differences(t, got, oracle, generated); len(d) != 2 {
			t.Fatalf("user errors that differ in a location suffix compared equal: %v", d)
		}
	})
	// The flow engine's templates keep their Rust locations through the
	// normaliser; at a generated value the rule strips them, one or two,
	// and nothing else: not a user's error, not a message outside the
	// template, not a location inside the message.
	template := func(name, msg string) string {
		b, _ := json.Marshal(map[string]map[string]string{"error": {"name": name, "message": msg}})
		return string(b)
	}
	for _, c := range []struct {
		name, ours, oracle string
		equal              bool
	}{
		{"pickup, two locations", template("Anyhow", "Error: Error: QuickJS evaluation error: Error: x\n    at eval_script:1:1\n"),
			template("Anyhow", "Error: Error: QuickJS evaluation error: Error: x\n    at eval_script:1:1\n @worker_flow.rs:2451:11 @worker.rs:3822:13"), true},
		{"pickup, one location", template("Anyhow", "Error: Bad request: no"), template("Anyhow", "Error: Bad request: no @worker.rs:3822:13"), true},
		{"chaining", template("InternalError", "Error: QuickJS evaluation error: y\n"), template("InternalError", "Error: QuickJS evaluation error: y\n @worker_flow.rs:2451:11"), true},
		{"all-iterations stop", template("EarlyStopError", "Error evaluating stop_after_all_iters_if expression `z`: Error: e\n"),
			template("EarlyStopError", "Error evaluating stop_after_all_iters_if expression `z`: Error: e\n @worker_flow.rs:2451:11"), true},
		{"a user's stop message", template("EarlyStopError", "halt"), template("EarlyStopError", "halt @a.rs:1:2"), false},
		{"a pickup message outside the template", template("Anyhow", "boom"), template("Anyhow", "boom @a.rs:1:2"), false},
		{"a location inside the message", template("InternalError", "a b"), template("InternalError", "a @a.rs:1:2 b"), false},
		{"another name", template("Error", "Error: x"), template("Error", "Error: x @a.rs:1:2"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, oracle := observe(t, c.ours), observe(t, c.oracle)
			if d := differences(t, got, oracle, generated); (len(d) == 0) != c.equal {
				t.Fatalf("differences %v", d)
			}
			if d := differences(t, got, oracle, nil); len(d) != 2 {
				t.Fatalf("differences without stripping: %v", d)
			}
		})
	}
	t.Run("a user's literal marker", func(t *testing.T) {
		// The normaliser escapes a literal "<loc>" in user text, so it is
		// never taken for the marker.
		got, oracle := observe(t, internal("")), observe(t, internal(" <loc>"))
		if !strings.HasSuffix(string(oracle.Result), ` sequence <<loc>"}}`) {
			t.Fatalf("the literal was not escaped: %s", oracle.Result)
		}
		if d := differences(t, got, oracle, generated); len(d) != 2 {
			t.Fatalf("an escaped literal was stripped: %v", d)
		}
	})
	t.Run("chaining errors and the stop string", func(t *testing.T) {
		// spec: FX-15/a, FX-15/b, FX-21/a, JE-35/b, FX-50
		const eval = `QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n`
		object := func(name, msg string) string { return `{"error":{"name":"` + name + `","message":"` + msg + `"}}` }
		str := func(msg string) string { return `{"error":"` + msg + `"}` }
		for _, c := range []struct {
			name, wl, oracle string
			equal            bool
		}{
			{"later transition, one suffix", object("InternalError", "Error: "+eval), object("InternalError", "Error: "+eval+" @worker_flow.rs:2451:11"), true},
			{"pickup, two prefixes and two suffixes", object("Anyhow", "Error: Error: "+eval), object("Anyhow", "Error: Error: "+eval+" @worker_flow.rs:2451:11 @worker.rs:3822:13"), true},
			{"pickup, a typed error", object("Anyhow", "Error: Timeout value cannot be negative"), object("Anyhow", "Error: Timeout value cannot be negative @worker.rs:3822:13"), true},
			// FX-50, masking step 3: a parenthesised location is not a
			// layer's suffix and is never found by searching the text; the
			// subtest "written as <loc>" has the expected text that masks it.
			{"the parenthesised form", object("Anyhow", "Error: not found"), object("Anyhow", "Error: not found (jobs.rs:9074)"), false},
			{"a throwing stop_after_if", str("Error: " + eval), str("Error: " + eval + " @worker_flow.rs:2451:11"), true},
			// One suffix per prefix, no more: a user's own text that ends
			// like a location stays.
			{"only as many as the prefixes", object("Anyhow", "Error: x @u.rs:1:2"), object("Anyhow", "Error: x @u.rs:1:2 @worker.rs:3822:13"), true},
			// At most one suffix per prefix: a layer built without a
			// location has a prefix and no suffix (FX-50, "Error:
			// Internal: <text><loc>" at a pickup).
			{"fewer suffixes than prefixes", object("Anyhow", "Error: Error: x"), object("Anyhow", "Error: Error: x @worker.rs:3822:13"), true},
			// spec: FX-50/b: every layer prefix counts, not only "Error: ".
			{"an internal error built without a location", object("Anyhow", "Error: Internal: x"), object("Anyhow", "Error: Internal: x @worker.rs:3822:13"), true},
			// spec: FX-50/a: a failed push is a database error, one layer
			// at a later transition and two at a pickup.
			{"a database error at a later transition", object("InternalError", "SqlErr: error returned from database: unsupported Unicode escape sequence"), object("InternalError", "SqlErr: error returned from database: unsupported Unicode escape sequence @jobs.rs:6450:14"), true},
			{"a database error at a pickup", object("Anyhow", "Error: SqlErr: error returned from database: unsupported Unicode escape sequence"), object("Anyhow", "Error: SqlErr: error returned from database: unsupported Unicode escape sequence @jobs.rs:6450:14 @worker.rs:3822:13"), true},
			{"a database error keeps a third suffix", object("Anyhow", "Error: SqlErr: d"), object("Anyhow", "Error: SqlErr: d @a.rs:1:2 @jobs.rs:6450:14 @worker.rs:3822:13"), false},
			// spec: FX-50/c: a suffix the user's message holds sits before
			// the engine's stack, not at the end, and stays.
			{"a user's suffix before the stack", object("InternalError", `Error: QuickJS evaluation error: Error: x @worker.rs:1:2\n    at <anonymous> (eval_script:1:20)\n`), object("InternalError", `Error: QuickJS evaluation error: Error: x @worker.rs:1:2\n    at <anonymous> (eval_script:1:20)\n @worker_flow.rs:2451:11`), true},
			{"a user's suffix is compared", object("InternalError", `Error: QuickJS evaluation error: Error: x\n    at <anonymous> (eval_script:1:20)\n`), object("InternalError", `Error: QuickJS evaluation error: Error: x @worker.rs:1:2\n    at <anonymous> (eval_script:1:20)\n @worker_flow.rs:2451:11`), false},
			// spec: FX-50/d: a not-found phrase in the user's own message is
			// compared literally, its parenthesised location included.
			{"a forged not-found phrase", object("InternalError", `Error: QuickJS evaluation error: Error: Not found: Completed Job not found at name pretend (user.rs:123)\n    at <anonymous> (eval_script:1:20)\n`), object("InternalError", `Error: QuickJS evaluation error: Error: Not found: Completed Job not found at name pretend (user.rs:456)\n    at <anonymous> (eval_script:1:20)\n @worker_flow.rs:2451:11`), false},
			{"the same forged phrase", object("InternalError", `Error: QuickJS evaluation error: Error: Not found: Completed Job not found at name pretend (user.rs:123)\n    at <anonymous> (eval_script:1:20)\n`), object("InternalError", `Error: QuickJS evaluation error: Error: Not found: Completed Job not found at name pretend (user.rs:123)\n    at <anonymous> (eval_script:1:20)\n @worker_flow.rs:2451:11`), true},
			{"a typed message has no prefix", object("InternalError", "Expected an integer, found: -1"), object("InternalError", "Expected an integer, found: -1 @worker.rs:1:2"), false},
			{"another error name", object("Error", "Error: x"), object("Error", "Error: x @worker.rs:3822:13"), false},
			// spec: JE-42/g, WR-5/b, FX-51/c: an argument resolution's
			// internal error in the execution envelope loses its one suffix,
			// and only that envelope.
			{"a resolution's internal error", object("ExecutionErr", `execution error:\nInternal: Argument `+"`x`"+` is an invalid resource path: pg`), object("ExecutionErr", `execution error:\nInternal: Argument `+"`x`"+` is an invalid resource path: pg @common.rs:278:20`), true},
			{"a resolution's not-found has no suffix", object("ExecutionErr", `execution error:\nNot found: x`), object("ExecutionErr", `execution error:\nNot found: x @common.rs:278:20`), false},
		} {
			got, oracle := observe(t, c.wl), observe(t, c.oracle)
			d := differences(t, got, oracle, generated)
			if (len(d) == 0) != c.equal {
				t.Errorf("%s: differences %v", c.name, d)
			}
			// Only where windmill-lite generated the value.
			if d := differences(t, got, oracle, nil); c.wl != c.oracle && len(d) == 0 {
				t.Errorf("%s: compared equal without the rule", c.name)
			}
		}
	})
	t.Run("written as <loc>", func(t *testing.T) {
		// spec: FX-50, CF-19/c: an expected text the case's author wrote,
		// already normalised, masks a parenthesised location by writing
		// <loc> for it, wherever it stands; a trailing <loc> counts as a
		// layer's suffix.
		const fetch = `Error during isolated evaluation of expression ` + "`[results.br]`" + `:\nQuickJS evaluation error: Error: Failed to fetch result for step 'br': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000`
		const frames = `\n    at __throwOrParse (eval_script:5:54)\n    at __getResult (eval_script:11:59)\n`
		expected := func(result string) Observation {
			v := json.RawMessage(result)
			return Observation{Status: RunFailure, Result: v, Steps: map[SitePath]StepObservation{
				ids[job]: {Status: StepFailure, Args: json.RawMessage(`{}`), Result: v},
			}}
		}
		for _, c := range []struct {
			name, wl, want string
			equal          bool
		}{
			{"inside a transform error", template("ExecutionErr", fetch+frames), template("ExecutionErr", fetch+" <loc>"+frames), true},
			{"at the end of a chaining error", template("Anyhow", "Error: not found"), template("Anyhow", "Error: not found <loc>"), true},
			{"with a layer's suffix after it", template("Anyhow", "Error: not found"), template("Anyhow", "Error: not found <loc> @worker.rs:3822:13"), true},
			{"the expected text writes the location out", template("ExecutionErr", fetch+frames), template("ExecutionErr", fetch+" (jobs.rs:9074)"+frames), false},
		} {
			got := observe(t, c.wl)
			if d := differences(t, got, expected(c.want), generated); (len(d) == 0) != c.equal {
				t.Errorf("%s: differences %v", c.name, d)
			}
			if d := differences(t, got, expected(c.want), nil); len(d) == 0 {
				t.Errorf("%s: compared equal without the rule", c.name)
			}
		}
	})
	t.Run("execution errors", func(t *testing.T) {
		// spec: WR-37, WR-43, D14
		object := func(name, msg string) string {
			return `{"error":{"message":"` + msg + `","name":"` + name + `","step_id":"a"}}`
		}
		const load = `execution error:\nError: failed to load module: SyntaxError: Cannot declare a const variable twice: 'BASE_URL'.`
		const internal = `execution error:\nInternal: Failed to parse $jsonvar value as JSON: unexpected end of hex escape at line 1 column 9`
		for _, c := range []struct {
			name, wl, oracle string
			equal            bool
		}{
			{"a native load failure", object("ExecutionErr", load), object("ExecutionErr", load+" @windmill-runtime-nativets/src/lib.rs:890:21"), true},
			{"a native transpile failure", object("ExecutionErr", `execution error:\nError: Expected ',', got '{'`), object("ExecutionErr", `execution error:\nError: Expected ',', got '{' @lib.rs:399:9`), true},
			{"an internal error", object("ExecutionErr", internal), object("ExecutionErr", internal+" @variables.rs:300:5"), true},
			{"no suffix to strip", object("ExecutionErr", `execution error:\nProxy is an EE feature`), object("ExecutionErr", `execution error:\nProxy is an EE feature`), true},
			// Only the rows the catalogue writes with a location.
			{"a message written without a location", object("ExecutionErr", `execution error:\nProxy is an EE feature`), object("ExecutionErr", `execution error:\nProxy is an EE feature @lib.rs:755:9`), false},
			// One suffix, no more.
			{"two suffixes", object("ExecutionErr", load), object("ExecutionErr", load+" @a.rs:1:2 @b.rs:3:4"), false},
			{"not an execution error's message", object("ExecutionErr", "Error: x"), object("ExecutionErr", "Error: x @worker.rs:1:2"), false},
			{"another error name", object("Error", load), object("Error", load+" @lib.rs:890:21"), false},
		} {
			got, oracle := observe(t, c.wl), observe(t, c.oracle)
			if d := differences(t, got, oracle, generated); (len(d) == 0) != c.equal {
				t.Errorf("%s: differences %v", c.name, d)
			}
			if d := differences(t, got, oracle, nil); c.wl != c.oracle && len(d) == 0 {
				t.Errorf("%s: compared equal without the rule", c.name)
			}
		}
	})
}

// TestNormaliseText normalises response bodies as text: the spelling is
// kept (spaces, member order), walked job ids become placeholders, a
// runtime error's stack becomes the marker where the body writes it as a
// JSON string, and nothing else is touched: another stack, an unwalked id,
// user text that looks like a marker (escaped, so the result stays
// injective).
func TestNormaliseText(t *testing.T) {
	const job = "10000000-0000-4000-8000-000000000002"
	stack := "Error: z\n    at main (/tmp/windmill/wk/" + job + "/main.ts:2:9)"
	stackJSON, _ := json.Marshal(stack)
	errJSON := `{"error":{"name":"Error","message":"z","stack":` + string(stackJSON) + `,"step_id":"a"}}`
	n := &Normaliser{
		IDs:           map[string]SitePath{job: SitePath{}.Child(Module("a"))},
		RuntimeErrors: []json.RawMessage{json.RawMessage(errJSON)},
	}
	for _, c := range []struct{ name, raw, want string }{
		{"spaced object", `{"a": 2, "b": 1}`, `{"a": 2, "b": 1}`},
		{"unquoted string", `ok`, `ok`},
		{"empty", ``, ``},
		{"job id", `{"job": "` + job + `"}`, `{"job": "<job:m:a>"}`},
		{"stack", `{"error": {"name": "Error", "message": "z", "stack": ` + string(stackJSON) + `, "step_id": "a"}}`, `{"error": {"name": "Error", "message": "z", "stack": "<stack>", "step_id": "a"}}`},
		{"another stack", `{"stack": "Error: y\n    at x"}`, `{"stack": "Error: y\n    at x"}`},
		{"unwalked id", `"20000000-0000-4000-8000-000000000009"`, `"20000000-0000-4000-8000-000000000009"`},
		{"marker-like text", `"<stack>"`, `"<<stack>"`},
		{"the stack alone, as a string", string(stackJSON), strings.Replace(string(stackJSON), job, "<job:m:a>", 1)},
		{"the inner error", `{"name": "Error", "message": "z", "stack": ` + string(stackJSON) + `, "step_id": "a"}`, `{"name": "Error", "message": "z", "stack": "<stack>", "step_id": "a"}`},
		{"the error in an array", `[1, {"step_id": "a", "error": {"stack": ` + string(stackJSON) + `, "step_id": "a", "message": "z", "name": "Error"}}]`, `[1, {"step_id": "a", "error": {"stack": "<stack>", "step_id": "a", "message": "z", "name": "Error"}}]`},
	} {
		got, err := n.Text(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestNormaliseTextKeepsHandlerData keeps a handler's own data as written
// although it holds a runtime error's stack string: only an object
// deep-equal to the runtime error, or to its inner error, has its stack
// masked, as Value masks it (OR-V3 clause a, and clause d's handler data).
func TestNormaliseTextKeepsHandlerData(t *testing.T) {
	n := &Normaliser{RuntimeErrors: []json.RawMessage{json.RawMessage(`{"error":{"name":"Error","message":"boom","stack":"trace-A"}}`)}}
	for _, raw := range []string{
		`{"note": "trace-A", "error": {"name": "UserData", "stack": "trace-A"}}`,
		`{"error": {"name": "Error", "message": "boom", "stack": "trace-A", "handled": true}}`,
		`{"saw": {"name": "Error", "message": "boom!", "stack": "trace-A"}}`,
		`"trace-A"`,
		`trace-A, not JSON`,
	} {
		got, err := n.Text(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != raw {
			t.Errorf("handler data changed: got %s, want %s", got, raw)
		}
	}
	for raw, want := range map[string]string{
		`{"error": {"name": "Error", "message": "boom", "stack": "trace-A"}}`:                `{"error": {"name": "Error", "message": "boom", "stack": "<stack>"}}`,
		`{"saw": {"stack": "trace-A", "name": "Error", "message": "boom"}}`:                  `{"saw": {"stack": "<stack>", "name": "Error", "message": "boom"}}`,
		`{"note": "trace-A", "e": {"name": "Error", "message": "boom", "stack": "trace-A"}}`: `{"note": "trace-A", "e": {"name": "Error", "message": "boom", "stack": "<stack>"}}`,
	} {
		got, err := n.Text(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

// TestResponseBytes reads a text and a base64 body back, and refuses an
// unknown encoding.
func TestResponseBytes(t *testing.T) {
	for _, c := range []struct {
		r    Response
		want []byte
	}{
		{Response{Body: "hi"}, []byte("hi")},
		{Response{Encoding: BodyBase64, Body: "/wD+"}, []byte{0xff, 0x00, 0xfe}},
	} {
		got, err := c.r.Bytes()
		if err != nil || string(got) != string(c.want) {
			t.Errorf("%+v: %x %v, want %x", c.r, got, err, c.want)
		}
	}
	if _, err := (Response{Encoding: "hex", Body: "ff"}).Bytes(); err == nil {
		t.Error("an unknown encoding was accepted")
	}
}
