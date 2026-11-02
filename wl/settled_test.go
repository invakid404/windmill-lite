package wl

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// heldProvider is a run's own resource and variable provider that holds
// every read until release closes, then answers that it has none, so the
// read goes on to the next provider of the run's chain. It counts the
// reads, which tells that the run's own chain answered them.
type heldProvider struct {
	release chan struct{}
	entered chan struct{}
	reads   atomic.Int32
}

func (h *heldProvider) hold() {
	h.reads.Add(1)
	h.entered <- struct{}{}
	<-h.release
}

func (h *heldProvider) Resource(context.Context, string) (platform.Resource, bool, error) {
	h.hold()
	return platform.Resource{}, false, nil
}

func (h *heldProvider) Variable(context.Context, string) (platform.Variable, bool, error) {
	h.hold()
	return platform.Variable{}, false, nil
}

// Calls in flight when a run settles (lever J6). A step's resource and
// variable reads, admitted by the facade on its TCP path and on its
// intercepted path, are held in the run's own provider until the
// invocation has been cancelled, has settled, and the run has settled.
// Let go, each is answered by the run's providers as before, its own and
// then its plugin's, whose binding refuses a settled run (errRunSettled):
// no host hook is called after settlement, and no read falls back to the
// runtime's providers, which hold the same paths with other values.
func TestSettledRunCallsInFlight(t *testing.T) {
	rt := openHooks(t)
	if err := rt.store.SetResource("u/x/r", platform.Resource{Type: "t", Value: []byte(`{"from":"the runtime"}`)}); err != nil {
		t.Fatal(err)
	}
	rt.store.SetVariable("u/x/v", platform.Variable{Value: "from the runtime"})
	var settledAt atomic.Pointer[time.Time]
	var hooks, lateHooks atomic.Int32
	call := func(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error) {
		hooks.Add(1)
		if settledAt.Load() != nil {
			lateHooks.Add(1)
		}
		return jsonval.FromProducer([]byte(`{"found":true,"type":"t","value":{"from":"the plugin"}}`)), nil
	}
	if err := rt.usePlugin(jsonval.FromProducer([]byte(`{"resource":true,"variable":true}`)), call, time.Minute, func(string) {}); err != nil {
		t.Fatal(err)
	}
	held := &heldProvider{release: make(chan struct{}), entered: make(chan struct{}, 4)}
	type reply struct {
		path   string
		status int
		body   string
	}
	replies := make(chan reply, 4)
	var intercepted []func(context.Context) harness.PlatformReply
	invDone, admitted := make(chan struct{}), make(chan struct{})
	settled := make(chan struct{})
	hooksOf := &Hooks{
		OnModuleStart: func(ctx context.Context, r RunHandle, s Site) error {
			if s.Module != "a" {
				return nil
			}
			// The run's own services as a run whose plugin answers reads has
			// them (pluginServices), its own provider first.
			bind := &runBinding{id: r.ID()}
			bind.started(ctx, r)
			pr, pv := rt.plugin.Load().platform(bind)
			services := rt.services.WithProviders(platform.Providers{
				Resources: platform.ResourceChain{held, pr, rt.store},
				Variables: platform.VariableChain{held, pv, rt.store},
			})
			invCtx, cancelInv := context.WithCancel(context.Background())
			exec := rt.fac.Executor(stepFunc(func(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
				for _, p := range []string{"/api/w/ws/resources/get_value_interpolated/u/x/r", "/api/w/ws/variables/get_value/u/x/v"} {
					// The intercepted path: admitted now, served later.
					intercepted = append(intercepted, rt.fac.Platform()(harness.PlatformRequest{ID: "p" + p, Inv: inv.ID, Method: "GET", URL: rt.fac.WarmOrigin() + p}))
					// The TCP path: admitted, then held in the run's provider.
					go func() {
						req, _ := http.NewRequest("GET", rt.fac.FreshOrigin()+p, nil)
						req.Header.Set("Authorization", "Bearer tok-j6")
						resp, err := http.DefaultClient.Do(req)
						if err != nil {
							replies <- reply{p, 0, err.Error()}
							return
						}
						b, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						replies <- reply{p, resp.StatusCode, string(b)}
					}()
				}
				<-held.entered
				<-held.entered
				close(admitted)
				<-ctx.Done()
				inv.Report.Settled(engine.SettleCompleted)
				return engine.StepOutcome{Kind: engine.OutcomeError, Body: jsonval.FromProducer([]byte(`{"name":"Canceled"}`))}, nil
			}))
			go func() {
				defer close(invDone)
				exec.Execute(invCtx, engine.Invocation{ID: "inv-j6", Run: r.ID(), Platform: services, Report: engine.NopReporter{},
					Env: map[string]string{"WM_TOKEN": "tok-j6", "WM_WORKSPACE": "ws", "WM_JOB_ID": "00000000-0000-4000-8000-0000000000f6"}})
			}()
			<-admitted
			cancelInv()
			<-invDone
			return nil
		},
		OnRunSettled: func(*Trace) {
			now := time.Now()
			settledAt.Store(&now)
			close(settled)
		},
	}
	res, err := rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: hooksOf})
	if err != nil || res.Status != StatusSuccess {
		t.Fatalf("%v %v", res, err)
	}
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never settled")
	}
	// Let the held reads go, and serve the intercepted ones.
	close(held.release)
	var wg sync.WaitGroup
	for i, serve := range intercepted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep := serve(context.Background())
			replies <- reply{[]string{"resource", "variable"}[i], rep.Status, string(rep.Body)}
		}()
	}
	wg.Wait()
	for range 4 {
		var r reply
		select {
		case r = <-replies:
		case <-time.After(5 * time.Second):
			t.Fatal("a held read never answered")
		}
		if r.status == 200 || !strings.Contains(r.body, "the run has settled") {
			t.Errorf("%s: %d %s; want the plugin's refusal of a settled run", r.path, r.status, r.body)
		}
	}
	if n := held.reads.Load(); n != 4 {
		t.Errorf("the run's own provider saw %d reads, want 4: a read left the run's providers", n)
	}
	if n := hooks.Load(); n != 0 {
		t.Errorf("%d host hooks were called for reads of a settled run (%d after settlement)", n, lateHooks.Load())
	}
}
