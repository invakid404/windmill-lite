package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The tests of what input transforms store from flow_env since Windmill
// 1.855.0 (JE-38): je/providers' je38-ab and je/je-38-env-refs record the
// same on CE, where the sealed arguments are masked; these check them
// unmasked.

// envRefs is the env of the JE-38 tests: V a variable that is not secret,
// S a secret one, K a literal, R a resource whose whole value is S's
// reference, C a variable whose value is V's reference.
const envRefs = `{"V":"$var:u/t/v","S":"$var:u/t/s","K":"lit","R":"$res:u/t/rs","C":"$var:u/t/c"}`

func envServices(t *testing.T) (*platform.Services, *platform.Store) {
	t.Helper()
	s := platform.NewStore()
	s.SetVariable("u/t/v", platform.Variable{Value: "plain"})
	s.SetVariable("u/t/s", platform.Variable{Value: "hidden", IsSecret: true})
	s.SetVariable("u/t/c", platform.Variable{Value: "$var:u/t/v"})
	if err := s.SetResource("u/t/rs", platform.Resource{Type: "object", Value: json.RawMessage(`"$var:u/t/s"`)}); err != nil {
		t.Fatal(err)
	}
	return &platform.Services{Resources: s, Variables: s}, s
}

// sealedArgs fails unless each named member of the recorded arguments is
// an $encrypted: value that windmill-lite sealed: standard padded base64
// that holds no plaintext. It returns the members.
func sealedArgs(t *testing.T, recorded jsonval.Value, names ...string) map[string]string {
	t.Helper()
	var args map[string]json.RawMessage
	if err := json.Unmarshal(recorded.Producer(), &args); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range names {
		var v string
		if err := json.Unmarshal(args[name], &v); err != nil {
			t.Fatalf("%s: %s is not a string", name, args[name])
		}
		blob, ok := strings.CutPrefix(v, "$encrypted:")
		raw, err := base64.StdEncoding.Strict().DecodeString(blob)
		if !ok || err != nil || strings.Contains(string(raw), "hidden") {
			t.Fatalf("%s is not sealed: %q", name, v)
		}
		out[name] = v
	}
	return out
}

// withSealed replaces the named members of args with "<sealed>".
func withSealed(t *testing.T, args jsonval.Value, names ...string) jsonval.Value {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args.Producer(), &m); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		m[n] = json.RawMessage(`"<sealed>"`)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return jsonval.FromProducer(b)
}

// JE-38/a, /b, /e, /f, /h, /i: at a step, an exact read stores the env's
// entry as written, secret or not, in both spellings, and the script job
// resolves it, terminally, so C reaches the script as V's reference; any
// other text stores the value from the resolved env, sealed under the
// root job's key when it reads a secret entry by name (with . or ?.), which
// the script job opens; R is not secret, and a computed key or a use of
// the whole object is not seen.
func TestFlowEnvStoredArguments(t *testing.T) {
	s := &scripts{}
	p, _ := envServices(t)
	e := openWithPlatform(t, map[string]string{"f/t/main": withEnv(flowOf(
		mod("b", "echo", inputs(
			"x", js(`flow_env.V`), "y", js(`flow_env["S"]`), "z", js(`flow_env.K`),
			"w", js(`flow_env.S + "!"`), "o", js(`flow_env?.S + "?"`), "q", js("`${flow_env.V}!`"),
			"r", js(`flow_env.R`), "rc", js(`flow_env.R + "!"`),
			"c", js(`flow_env.C`), "cc", js("`${flow_env.C}`"),
			"n", js(`flow_env["S".trim()]`), "j", js(`JSON.stringify(flow_env).includes("hidden")`),
			"st", static(`"$var:u/t/s"`),
		), ""),
	), envRefs)}, s, p)
	tr := runIt(t, e, `{}`, nil)
	b := step(t, tr, "b")
	if tr.Status != conformance.RunSuccess || len(s.invs) != 1 {
		t.Fatalf("%s %s", tr.Status, tr.Result)
	}
	first := sealedArgs(t, b.RecordedArgs, "w", "o")
	sameJSON(t, "O2", withSealed(t, b.RecordedArgs, "w", "o"), `{"x":"$var:u/t/v","y":"$var:u/t/s","z":"lit","w":"<sealed>","o":"<sealed>","q":"plain!",`+
		`"r":"$res:u/t/rs","rc":"hidden!","c":"$var:u/t/c","cc":"$var:u/t/v","n":"hidden","j":true,"st":"$var:u/t/s"}`)
	want := `{"x":"plain","y":"hidden","z":"lit","w":"hidden!","o":"hidden?","q":"plain!","r":"hidden","rc":"hidden!","c":"$var:u/t/v","cc":"plain","n":"hidden","j":true,"st":"hidden"}`
	sameJSON(t, "O3", b.ResolvedArgs, want)
	sameJSON(t, "the script's arguments", s.invs[0].Args, want)

	// A value is sealed anew for every run, under that run's root.
	again := sealedArgs(t, step(t, runIt(t, e, `{}`, nil), "b").RecordedArgs, "w", "o")
	if again["w"] == first["w"] || again["o"] == first["o"] || first["w"] == first["o"] {
		t.Fatalf("the sealed values repeat: %v %v", first, again)
	}
}

// JE-38/g: into a subflow called by path only a secret entry keeps its
// reference: the subflow's input holds the others resolved, R's included,
// and the computed value sealed; the subflow's steps resolve the
// reference and open the value, since they share the root job.
func TestFlowEnvIntoSubflow(t *testing.T) {
	s := &scripts{}
	p, _ := envServices(t)
	e := openWithPlatform(t, map[string]string{
		"f/t/child": flowOf(mod("e", "echo", inputs("x", js("flow_input.x"), "y", js("flow_input.y"), "w", js("flow_input.w"), "k", js("flow_input.k"), "r", js("flow_input.r")), "")),
		"f/t/main": withEnv(flowOf(
			subflowOf("c", "f/t/child", inputs("x", js("flow_env.V"), "y", js("flow_env.S"), "w", js(`flow_env.S + "!"`), "k", js("flow_env.K"), "r", js("flow_env.R")), ""),
		), envRefs),
	}, s, p)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"x":"plain","y":"hidden","w":"hidden!","k":"lit","r":"hidden"}`)
	c, inner := at(t, tr, "m:c"), at(t, tr, "m:c/m:e")
	sealed := sealedArgs(t, c.RecordedArgs, "w")
	sameJSON(t, "the subflow's input", withSealed(t, c.RecordedArgs, "w"), `{"x":"plain","y":"$var:u/t/s","w":"<sealed>","k":"lit","r":"hidden"}`)
	if got := sealedArgs(t, inner.RecordedArgs, "w"); got["w"] != sealed["w"] {
		t.Fatalf("the subflow's step stored %q, its input holds %q", got["w"], sealed["w"])
	}
	sameJSON(t, "the subflow step's O3", inner.ResolvedArgs, `{"x":"plain","y":"hidden","w":"hidden!","k":"lit","r":"hidden"}`)
}

// JE-38, FX-46: a collapsed loop body's transforms, and the failure
// module's, store as a step's do.
func TestFlowEnvCollapsedBodyAndFailureModule(t *testing.T) {
	s := &scripts{}
	p, _ := envServices(t)
	e := openWithPlatform(t, map[string]string{
		// The loop collapses only in a flow with no failure module (CF-8).
		"f/t/main": withEnv(flowOf(
			loopOf("l", []string{mod("b", "echo", inputs("x", js("flow_env.V"), "w", js(`flow_env.S + "!"`)), "")}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""),
		), envRefs),
		"f/t/failing": withEnv(withFailure(
			handler("echo", inputs("x", js("flow_env.S"), "w", js(`flow_env.S + "!"`)), ""),
			mod("a", "fail", "", ""),
		), envRefs),
	}, s, p)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `[{"x":"plain","w":"hidden!"}]`)
	if len(tr.Children) != 0 {
		t.Fatalf("the loop did not collapse: %+v", tr.Children)
	}
	b := at(t, tr, "m:l/i:0/m:b")
	sealedArgs(t, b.RecordedArgs, "w")
	sameJSON(t, "the collapsed body's O2", withSealed(t, b.RecordedArgs, "w"), `{"x":"$var:u/t/v","w":"<sealed>"}`)

	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/failing", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := failureStep(tr)
	if !ok {
		t.Fatal("no failure module ran")
	}
	sealedArgs(t, f.RecordedArgs, "w")
	sameJSON(t, "the failure module's O2", withSealed(t, f.RecordedArgs, "w"), `{"x":"$var:u/t/s","w":"<sealed>"}`)
	sameJSON(t, "the failure module's O3", f.ResolvedArgs, `{"x":"hidden","w":"hidden!"}`)
}

// JE-38 at a host-provided site (ADR 0002, ADR 0008): its transforms store
// what its target's step would, kept references included, except that
// nothing is sealed, since the provider runs outside the engine and could
// not open it: the provider gets the computed value in clear.
func TestFlowEnvAtProvidedSites(t *testing.T) {
	flows := map[string]string{"f/t/main": withEnv(flowOf(
		mod("p", "permit", inputs("x", js("flow_env.S"), "w", js(`flow_env.S + "!"`)), ""),
		subflowOf("q", "f/t/remote", inputs("x", js("flow_env.V"), "y", js("flow_env.S"), "w", js(`flow_env.S + "!"`)), ""),
	), envRefs)}
	prog := compileContract(t, flows, `{"roots":[{"flow":"f/t/main"}],"provided":[`+
		`{"selector":{"flow":"f/t/main","module":"p"},"provider":"host","mandatory":true},`+
		`{"selector":{"flow":"f/t/main","module":"q"},"provider":"host","mandatory":true}]}`)
	var mu sync.Mutex
	got := map[string]string{}
	provide := func(_ context.Context, _ Run, s Site, _ string, args jsonval.Value) (StepOutcome, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		got[s.Module] = string(args.Producer())
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, true, nil
	}
	p, _ := envServices(t)
	e, err := New(Config{Program: prog, Exec: &scripts{}, JS: jsEng, Provide: provide, Platform: p})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, "")
	sameJSON(t, "the script site's arguments", jsonval.FromProducer([]byte(got["p"])), `{"x":"$var:u/t/s","w":"hidden!"}`)
	sameJSON(t, "the flow site's arguments", jsonval.FromProducer([]byte(got["q"])), `{"x":"plain","y":"$var:u/t/s","w":"hidden!"}`)
}

// downVariables fails the lookup of u/t/down.
type downVariables struct{ platform.Variables }

func (d downVariables) Variable(ctx context.Context, path string) (platform.Variable, bool, error) {
	if path == "u/t/down" {
		return platform.Variable{}, false, errors.New("vault down")
	}
	return d.Variables.Variable(ctx, path)
}

// JE-38: an entry whose secrecy cannot be looked up is taken as secret, so
// a failed lookup never stores a value read from it in clear.
func TestFlowEnvFailedSecrecyLookup(t *testing.T) {
	s := &scripts{}
	p, store := envServices(t)
	p.Variables = downVariables{store}
	e := openWithPlatform(t, map[string]string{"f/t/main": withEnv(flowOf(
		mod("b", "echo", inputs("w", js(`flow_env.D + "!"`)), ""),
	), `{"D":"$var:u/t/down"}`)}, s, p)
	tr := runIt(t, e, `{}`, nil)
	b := step(t, tr, "b")
	sealedArgs(t, b.RecordedArgs, "w")
	// The env stayed unresolved, with a warning, and the script job opens
	// the value computed from it.
	sameJSON(t, "O3", b.ResolvedArgs, `{"w":"$var:u/t/down!"}`)
	if len(tr.Warnings) == 0 || tr.Warnings[0].Code != "flow_env_unresolved" {
		t.Fatalf("warnings %v", tr.Warnings)
	}
}

// JE-38: which texts read an entry by name.
func TestReadsByName(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{`flow_env.token`, true},
		{`"Bearer " + flow_env.token`, true},
		{`flow_env?.token`, true},
		{`(flow_env.token)`, true},
		{`flow_env.token.trim()`, true},
		{`flow_env["token"]`, true},
		{`flow_env['token'] + 1`, true},
		{"flow_env[`token`]", true},
		{`flow_env?.["token"]`, true},
		{`x+flow_env.token`, true},
		{`flow_env.tokens`, false},
		{`flow_env.token_2`, false},
		{`flow_env.token$`, false},
		{`flow_env.tokené`, false},
		{`my_flow_env.token`, false},
		{`$flow_env.token`, false},
		{`flow_env[k]`, false},
		{`flow_env["tok" + "en"]`, false},
		{`flow_env["token" ]`, false},
		{`JSON.stringify(flow_env)`, false},
		{`const {token} = flow_env; token`, false},
		{`my_flow_env.token + flow_env.token`, true},
		{`flow_input.token`, false},
	} {
		if got := readsByName(c.text, "token"); got != c.want {
			t.Errorf("%s: %v, want %v", c.text, got, c.want)
		}
	}
}
