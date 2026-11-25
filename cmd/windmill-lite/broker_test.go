package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// tokenBoundEnv names the bound of gone tokens that the engine keeps
// before it drains (tokenBound).
const tokenBoundEnv = "WL_TEST_PLUGIN_TOKEN_BOUND"

// installTokenBound sets tokenBound from tokenBoundEnv.
func installTokenBound() {
	if v := os.Getenv(tokenBoundEnv); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			panic(err)
		}
		tokenBound = n
	}
}

// brokerFiles and brokerContract are a bundle whose flow f/broker/prov has
// a mandatory site of the named provider bp.
var brokerFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/broker/label.ts": `// Returns its label.
export async function main(label: string) {
  return label;
}
`,
	"f/broker/prov.flow/flow.yaml": `{"summary":"a provided step","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/broker/label",` +
		`"input_transforms":{"label":{"type":"static","value":"step"}}}}]}}`,
	"f/broker/label.flow/flow.yaml": `{"summary":"one step","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/broker/label",` +
		`"input_transforms":{"label":{"type":"static","value":"step"}}}}]}}`,
}

const brokerContract = `roots:
  - flow: f/broker/prov
  - flow: f/broker/label
provided:
  - selector: {flow: f/broker/prov, module: a}
    provider: bp
    mandatory: true
`

// TestPluginBrokerHooks checks the engine's wiring of the runs' hooks on
// go-plugin's broker: a configuration with a provider set to broker
// starts, its provider is answered by the hooks of the run that reaches
// it, and a run's interception is answered by its host through the
// Client that Launch dispenses.
func TestPluginBrokerHooks(t *testing.T) {
	bun := absBun(t)
	out := filepath.Join(t.TempDir(), "bundle")
	if err := buildFolder(bun, brokerFiles, brokerContract, out); err != nil {
		t.Fatal(err)
	}
	cfg := pluginConfigOf(t, bun, out)
	cfg["providers"] = map[string]any{"bp": "broker"}
	e := launchPlugin(t, cfg)
	answer := func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		switch c.GetHook() {
		case "provide":
			return &plugin.HookReply{Payload: []byte(`{"result": "provided by the host"}`)}, nil
		case "intercept":
			return &plugin.HookReply{Payload: []byte(`{"result": "intercepted by the host"}`)}, nil
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := e.runner.RunWithHooks(ctx, &plugin.RunRequest{Path: "f/broker/prov", Args: []byte(`{}`)}, []byte(`{"providers": ["bp"]}`), answer)
	if err != nil || res.GetStatus() != "success" || string(res.GetResult()) != `"provided by the host"` {
		t.Errorf("the provided site: %v, %v; want the host's answer\nthe engine's standard error:\n%s", res, err, e.stderr)
	}
	decl := `{"intercept": [{"selector": {"flow": "f/broker/label", "module": "a"}}]}`
	res, err = e.runner.RunWithHooks(ctx, &plugin.RunRequest{Path: "f/broker/label", Args: []byte(`{}`)}, []byte(decl), answer)
	if err != nil || res.GetStatus() != "success" || string(res.GetResult()) != `"intercepted by the host"` {
		t.Errorf("the interception: %v, %v; want the host's answer\nthe engine's standard error:\n%s", res, err, e.stderr)
	}
}

// TestPluginTokenBoundDrains checks that the engine drains once it keeps
// its bound of tokens that GetRun answered gone for, lowered to 2 by a
// test hook. A run with hooks waits in its step meanwhile, which keeps the
// draining engine up: after one gone answer the engine serves; after the
// second it names its bound on standard error, answers health NOT_SERVING
// and refuses a new run. Released, the waiting run succeeds, the engine
// exits, and the Client holds no registration.
func TestPluginTokenBoundDrains(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out), tokenBoundEnv+"=2")
	runner := plugin.NewRunnerClient(e.conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir, args := newGate(t)
	hooks := func(context.Context, *plugin.HookCall) (*plugin.HookReply, error) {
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}
	type outcome struct {
		res *plugin.RunResult
		err error
	}
	held := make(chan outcome, 1)
	go func() {
		res, err := e.runner.RunWithHooks(ctx, &plugin.RunRequest{Id: "held", Path: "f/plugin/gate", Args: args}, []byte(`{"on_run_end": true}`), hooks)
		held <- outcome{res, err}
	}()
	waitFile(t, dir, "entered")
	lookup := func(token string) {
		t.Helper()
		res, err := runner.GetRun(ctx, &plugin.GetRunRequest{Id: "r-" + token, Token: token})
		if err != nil || res.GetState() != "gone" {
			t.Fatalf("GetRun of an unknown token: %v, %v; want gone", res, err)
		}
	}
	lookup("first")
	if st, err := e.runner.Health(ctx); err != nil || st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health after one gone answer: %v, %v; want SERVING", st, err)
	}
	lookup("second")
	for {
		st, err := e.runner.Health(ctx)
		if err == nil && st == healthpb.HealthCheckResponse_NOT_SERVING {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("the engine still serves at its bound (health %v, %v)\nits standard error:\n%s", st, err, e.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := e.stderr.String(); !strings.Contains(s, "bound of tokens") {
		t.Errorf("the engine's standard error does not name its bound:\n%s", s)
	}
	if _, err := e.runner.RunWithHooks(ctx, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x": 1}`)}, []byte(`{"on_run_end": true}`), hooks); !errors.Is(err, plugin.ErrNotStarted) {
		t.Errorf("a new run while draining: %v, want ErrNotStarted", err)
	}
	release(t, dir)
	if o := <-held; o.err != nil || o.res.GetStatus() != "success" {
		t.Errorf("the run that waited through the drain: %v, %v; want success", o.res, o.err)
	}
	for !e.client.Exited() {
		if ctx.Err() != nil {
			t.Fatalf("the drained engine did not exit\nits standard error:\n%s", e.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for e.runner.Registrations() != 0 {
		if ctx.Err() != nil {
			t.Fatalf("%d registrations after the engine exited", e.runner.Registrations())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
