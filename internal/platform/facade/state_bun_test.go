package facade

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// stateRig is a facade over a memory store, with f/t/held a provider's
// resource, and an executor of attr/state in one module state.
type stateRig struct {
	r     *bunRig
	f     *Facade
	x     engine.StepExecutor
	mode  engine.ModuleState
	store platform.ResourceStore
}

func newStateRig(t *testing.T, r *bunRig, mode engine.ModuleState, store platform.ResourceStore) *stateRig {
	t.Helper()
	provided := platform.NewStore()
	if err := provided.SetResource("f/t/held", platform.Resource{Type: "state", Value: json.RawMessage(`{"h":1}`)}); err != nil {
		t.Fatal(err)
	}
	f := newFacade(t, Config{Platform: &platform.Services{Resources: provided, ResourceStore: store}})
	return &stateRig{r: r, f: f, x: f.Executor(r.pool(t, f, mode)), mode: mode, store: store}
}

// step runs attr/state once as a step of flow, with args, and returns its
// outcome's kind and body.
func (s *stateRig) step(t *testing.T, flow, args string) (engine.OutcomeKind, string) {
	t.Helper()
	out, err := s.x.Execute(s.r.ctx(t), s.r.invocation(s.r.identity(flow, "a"), "attr/state", args, s.mode))
	if err != nil {
		t.Fatalf("%s: %v\n%s", args, err, s.r.diag)
	}
	return out.Kind, string(out.Body.Producer())
}

// TestStateOnTheClient runs the stock client's getState and setState, and
// its generated resource service, against the resource store in warm and
// fresh mode (WA-13): a state never written is undefined; a counter at the
// default state path counts across invocations; a state at an explicit
// path is shared; setState(undefined) stores null, on a missing state and
// on a present one; a create at a taken path rejects with the recorded
// ApiError; an update of a provider's resource is the restriction's 501,
// which fails the step in the default fail mode (ADR 0001, section 5a);
// and with a failing store getState throws, never resolving undefined.
func TestStateOnTheClient(t *testing.T) {
	r := newBunRig(t)
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		t.Run(string(mode), func(t *testing.T) {
			s := newStateRig(t, r, mode, platform.NewMemoryResourceStore())
			for i, c := range []struct{ flow, args, want string }{
				{"f/t/counter", `{"op":"get"}`, `{"value":"undefined"}`},
				{"f/t/counter", `{"op":"counter"}`, `{"value":{"before":0,"after":1}}`},
				{"f/t/counter", `{"op":"counter"}`, `{"value":{"before":1,"after":2}}`},
				{"f/t/counter", `{"op":"counter"}`, `{"value":{"before":2,"after":3}}`},
				{"f/t/counter", `{"op":"set"}`, `{"value":"undefined"}`},
				{"f/t/counter", `{"op":"get"}`, `{"value":null}`},
				{"f/t/one", `{"op":"set","path":"f/t/explicit","value":{"a":[1,"x"]}}`, `{"value":"undefined"}`},
				{"f/t/two", `{"op":"get","path":"f/t/explicit"}`, `{"value":{"a":[1,"x"]}}`},
				{"f/t/one", `{"op":"set","path":"f/t/unset"}`, `{"value":"undefined"}`},
				{"f/t/two", `{"op":"get","path":"f/t/unset"}`, `{"value":null}`},
				{"f/t/two", `{"op":"get","path":"f/t/never"}`, `{"value":"undefined"}`},
				{"f/t/one", `{"op":"create","path":"f/t/explicit","value":1}`, `{"thrown":"Bad Request","status":400,"body":"Bad request: Resource f/t/explicit already exists"}`},
				{"f/t/one", `{"op":"delete","path":"f/t/explicit"}`, `{"value":"resource f/t/explicit deleted"}`},
				{"f/t/two", `{"op":"get","path":"f/t/explicit"}`, `{"value":"undefined"}`},
			} {
				if kind, got := s.step(t, c.flow, c.args); kind != engine.OutcomeOK || got != c.want {
					t.Fatalf("#%d %s: %s %s, want %s\n%s", i, c.args, kind, got, c.want, r.diag)
				}
			}
			kind, got := s.step(t, "f/t/one", `{"op":"update","path":"f/t/held","value":1}`)
			const held = UnsupportedPrefix + "POST /w/{ws}/resources/update_value/{path}: the resource is provided by the host or the bundle, which windmill-lite does not write"
			var failed struct{ Name, Message string }
			if json.Unmarshal([]byte(got), &failed) != nil || kind != engine.OutcomeError || failed.Name != "UnsupportedPlatformCall" || failed.Message != held {
				t.Errorf("an update of a provider's resource: %s %s, want UnsupportedPlatformCall %q", kind, got, held)
			}

			broken := newStateRig(t, r, mode, brokenStore{})
			kind, got = broken.step(t, "f/t/counter", `{"op":"get"}`)
			var thrown struct{ Thrown string }
			if json.Unmarshal([]byte(got), &thrown) != nil || kind != engine.OutcomeOK || !strings.HasSuffix(thrown.Thrown, ": Internal: windmill-lite: the resource store failed: i/o timeout") {
				t.Errorf("getState on a failing store: %s %s, want the store's 500 thrown", kind, got)
			}
		})
	}
}

// TestConcurrentStateOnTheClient: 32 invocations at once in warm mode each
// set one state to a value of their own, which names its writer and
// repeats the name to 64 KiB, and read it back; every read is one whole
// value that one of them wrote.
func TestConcurrentStateOnTheClient(t *testing.T) {
	r := newBunRig(t)
	s := newStateRig(t, r, engine.ModuleStateWarm, platform.NewMemoryResourceStore())
	const n, size = 32, 64 << 10
	var wg sync.WaitGroup
	got := make([]string, n)
	kinds := make([]engine.OutcomeKind, n)
	for i := range n {
		wg.Go(func() {
			id := fmt.Sprintf("w%02d.", i)
			args, _ := json.Marshal(map[string]any{"op": "setget", "path": "f/t/conc", "value": map[string]any{"id": id, "n": size / len(id), "body": strings.Repeat(id, size/len(id))}})
			out, err := s.x.Execute(r.ctx(t), r.invocation(r.identity("f/t/conc", "a"), "attr/state", string(args), engine.ModuleStateWarm))
			if err != nil {
				got[i] = err.Error()
				return
			}
			kinds[i], got[i] = out.Kind, string(out.Body.Producer())
		})
	}
	wg.Wait()
	conflicts := 0
	for i := range n {
		var res struct {
			Value *struct {
				ID, Body string
				N        int
			}
			Thrown string
			Body   string
		}
		if kinds[i] != engine.OutcomeOK || json.Unmarshal([]byte(got[i]), &res) != nil {
			t.Errorf("invocation %d: %s %.200s\n%s", i, kinds[i], got[i], r.diag)
			continue
		}
		if res.Value == nil {
			// Creates that race: the stock client's setState checks, then
			// creates, so of those that saw no state one creates and the
			// others reject with the recorded conflict (state-client race).
			if res.Body != "Bad request: Resource f/t/conc already exists" {
				t.Errorf("invocation %d: %.200s", i, got[i])
			}
			conflicts++
			continue
		}
		v := res.Value
		if len(v.ID) != 4 || !strings.HasPrefix(v.ID, "w") || v.Body != strings.Repeat(v.ID, v.N) {
			t.Errorf("invocation %d read a value that is not one whole written value: id %q, %d bytes", i, v.ID, len(v.Body))
		}
	}
	if conflicts == n {
		t.Error("every invocation's setState rejected")
	}
	t.Logf("%d of %d setState calls lost the create race", conflicts, n)
}
