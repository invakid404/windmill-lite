package wl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runDataReport is what lateRunDataPlugin posts: which run, flow and point
// of it, and what the plugin read there as wl.run.data.
type runDataReport struct {
	Run, Flow, Phase, Status string
	Data                     json.RawMessage
}

// runDataReports collects the reports that the plugin posts.
type runDataReports struct {
	mu  sync.Mutex
	got []runDataReport
	srv *httptest.Server
}

func newRunDataReports(t *testing.T) *runDataReports {
	r := &runDataReports{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var x runDataReport
		if err := json.NewDecoder(req.Body).Decode(&x); err == nil {
			r.mu.Lock()
			r.got = append(r.got, x)
			r.mu.Unlock()
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// wait returns the first report that match accepts, waiting up to 30 s.
func (r *runDataReports) wait(t *testing.T, what string, match func(runDataReport) bool) runDataReport {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		r.mu.Lock()
		for _, x := range r.got {
			if match(x) {
				r.mu.Unlock()
				return x
			}
		}
		got := append([]runDataReport(nil), r.got...)
		r.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no report of %s; got %+v", what, got)
		}
	}
}

// has reports whether a report that match accepts has come.
func (r *runDataReports) has(match func(runDataReport) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.got {
		if match(x) {
			return true
		}
	}
	return false
}

// at matches the report of flow at phase.
func at(flow, phase string) func(runDataReport) bool {
	return func(x runDataReport) bool { return x.Flow == flow && x.Phase == phase }
}

// settledWith matches the onRunSettled report of a run that ended with
// status.
func settledWith(status string) func(runDataReport) bool {
	return func(x runDataReport) bool { return x.Phase == "settled" && x.Status == status }
}

// lateRunDataFiles is a folder of three flows of one step each, with a
// host plugin that posts what it reads as wl.run.data to report. f/lt/quick
// has an around wrapper, which posts before and after its step. The
// plugin's onModuleStart, at the step of f/lt/slow or f/lt/exit, posts,
// waits until a file named after the flow exists in gates, and posts again;
// at f/lt/exit it then ends its harness process. onModuleEnd and
// onRunSettled post too. With a short plugin timeout, a waiting
// onModuleStart latches its run, which then returns, and holds the run's
// settlement until it ends.
func lateRunDataFiles(report, gates string) map[string]string {
	flow := func(id string) string {
		return `{"summary":"l","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/lt/step",` +
			`"input_transforms":{"flow":{"type":"static","value":"` + id + `"}}}}]}}`
	}
	return map[string]string{
		"wmill.yaml":   "defaultTs: bun\n",
		"f/lt/step.ts": "export async function main(flow: string) {\n  return flow;\n}\n",
		"f/lt/plugin.ts": `import { existsSync } from "node:fs";
const report = (r: Record<string, unknown>) =>
  fetch(` + "`" + report + "`" + `, { method: "POST", body: JSON.stringify({ ...r, data: (globalThis as any).wl.run.data ?? "undefined" }) });
const wrap = async (c: { run: string; args: Record<string, unknown> }, next: () => Promise<unknown>) => {
  await report({ run: c.run, flow: String(c.args.flow), phase: "before" });
  await next();
  await report({ run: c.run, flow: String(c.args.flow), phase: "after" });
};
const short = (site: { flow: string }) => site.flow.slice("f/lt/".length);
export default {
  around: [{ selector: { flow: "f/lt/quick", module: "a" }, fn: wrap }],
  onModuleStart: async (e: { run: string; site: { flow: string } }) => {
    const flow = short(e.site);
    if (flow === "quick") return;
    await report({ run: e.run, flow, phase: "start" });
    while (!existsSync(` + "`" + gates + "/${flow}`" + `)) await Bun.sleep(10);
    if (flow === "exit") process.reallyExit(0);
    await report({ run: e.run, flow, phase: "start_end" });
  },
  onModuleEnd: async (e: { run: string; site: { flow: string } }) => {
    await report({ run: e.run, flow: short(e.site), phase: "module_end" });
  },
  onRunSettled: async (t: { run: string; status: string }) => {
    await report({ run: t.run, phase: "settled", status: t.status });
  },
};
`,
		"f/lt/slow.flow/flow.yaml":  flow("slow"),
		"f/lt/quick.flow/flow.yaml": flow("quick"),
		"f/lt/exit.flow/flow.yaml":  flow("exit"),
	}
}

const lateRunDataContract = "roots: [{flow: f/lt/slow}, {flow: f/lt/quick}, {flow: f/lt/exit}]\nplugin: f/lt/plugin.ts\n"

// openLate builds lateRunDataFiles and opens a runtime with one harness
// process and a plugin timeout of 500 ms. Writing gates/<flow> lets the
// waiting onModuleStart at that flow go on.
func openLate(t *testing.T) (rt *Runtime, reports *runDataReports, gates string) {
	t.Helper()
	bun := bunOrSkip(t)
	reports = newRunDataReports(t)
	gates = t.TempDir()
	out, runtimeDir := buildContractBundle(t, bun, lateRunDataFiles(reports.srv.URL, gates), lateRunDataContract)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 1}, PluginTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	// Every waiting hook goes on before the runtime closes.
	t.Cleanup(func() {
		for _, f := range []string{"slow", "exit"} {
			os.WriteFile(filepath.Join(gates, f), nil, 0o600)
		}
	})
	return rt, reports, gates
}

// TestRunDataReusedID: a run reuses the id of an earlier run that returned
// but has not settled, because its plugin hook, which outlived the plugin
// timeout, still waits in the same process. The later run reads its own
// data, or undefined when it has none, and settles first. The earlier run's
// plugin code then reads its own data: the waiting hook as it goes on, and
// its onRunSettled.
func TestRunDataReusedID(t *testing.T) {
	for _, c := range []struct{ name, data, want string }{
		{"later run with data", `{"tag":"b"}`, `{"tag":"b"}`},
		{"later run without data", "", `"undefined"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt, reports, gates := openLate(t)
			const id = "0b6f0e2c-0000-4000-8000-0000000000aa"
			resA, err := rt.Run(context.Background(), "f/lt/slow", nil, RunOptions{ID: id, Data: json.RawMessage(`{"tag":"a"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if resA.Status != StatusLatched {
				t.Fatalf("the earlier run: status %s, want latched by its plugin timeout", resA.Status)
			}
			resB, err := rt.Run(context.Background(), "f/lt/quick", nil, RunOptions{ID: id, Data: json.RawMessage(c.data)})
			if err != nil || resB.Status != StatusSuccess {
				t.Fatalf("the later run: %v, %+v", err, resB)
			}
			reports.wait(t, "the later run's settlement", settledWith(StatusSuccess))
			if reports.has(settledWith(StatusLatched)) {
				t.Fatal("the earlier run settled while its hook waited")
			}
			if err := os.WriteFile(filepath.Join(gates, "slow"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, w := range []struct {
				what  string
				match func(runDataReport) bool
				want  string
			}{
				{"the later run's wrapper before its step", at("quick", "before"), c.want},
				{"the later run's wrapper after its step", at("quick", "after"), c.want},
				{"the later run's onModuleEnd", at("quick", "module_end"), c.want},
				{"the later run's onRunSettled", settledWith(StatusSuccess), c.want},
				{"the earlier run's hook as it began", at("slow", "start"), `{"tag":"a"}`},
				{"the earlier run's hook as it went on", at("slow", "start_end"), `{"tag":"a"}`},
				{"the earlier run's onRunSettled", settledWith(StatusLatched), `{"tag":"a"}`},
			} {
				if got := reports.wait(t, w.what, w.match); !sameJSON(got.Data, json.RawMessage(w.want)) {
					t.Errorf("%s read %s, want %s", w.what, got.Data, w.want)
				}
			}
		})
	}
}

// TestRunDataOwnsBuffer: a run returns while its plugin hook, which
// outlived the plugin timeout, still waits, and the caller then writes
// other data into the buffer it gave as RunOptions.Data. The hook then ends
// its harness process, so the run's onRunSettled goes to a new process,
// whose frame carries the run's data: the hook reads the data as the run
// began.
func TestRunDataOwnsBuffer(t *testing.T) {
	rt, reports, gates := openLate(t)
	buf := []byte(`{"tag":"a"}`)
	res, err := rt.Run(context.Background(), "f/lt/exit", nil, RunOptions{Data: buf})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched {
		t.Fatalf("status %s, want latched by its plugin timeout", res.Status)
	}
	pid := 0
	if stats := rt.pool.Stats(); len(stats) == 1 {
		pid = stats[0].PID
	}
	copy(buf, strings.Replace(string(buf), `"a"`, `"z"`, 1))
	if reports.has(settledWith(StatusLatched)) {
		t.Fatal("the run settled while its hook waited")
	}
	if err := os.WriteFile(filepath.Join(gates, "exit"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := reports.wait(t, "the run's onRunSettled", settledWith(StatusLatched))
	if !sameJSON(got.Data, json.RawMessage(`{"tag":"a"}`)) {
		t.Errorf("onRunSettled read %s, want the data as the run began", got.Data)
	}
	if stats := rt.pool.Stats(); pid == 0 || len(stats) != 1 || stats[0].PID == pid {
		t.Errorf("onRunSettled did not go to a new process: before %d, now %+v", pid, stats)
	}
}
