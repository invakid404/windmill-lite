// A Bun host for the host-mode benchmarks (cmd/windmill-lite/bench_test.go,
// ADR 0005's benchmark plan): it embeds the host SDK, connects the engine
// on the bundle, and does nothing else, so that its process is a warm Bun
// 1.3.10 host whose only work is the steps the engine invokes in it. The
// benchmarks compile it with `bun build --compile`, as a host's own program
// is compiled, and submit the runs from the engine's side (the SDK's engine
// is the benchmark's process, through the engine proxy of bench_test.go),
// as `wltest check --mode host` does, so that they read each run's trace.
//
// With WL_BENCH_PLUGIN=1 it connects with a plugin, as a host that keeps
// its resources and variables elsewhere does: its resource and variable
// hooks answer the synthetic resources f/bench/r1 to f/bench/r4 and the
// variable f/bench/v1, and the resources under f/bench/slow/ after 1 ms,
// as an API 1 ms away would. It counts the hooks' calls by run and path,
// which the benchmarks' probe step reads
// (globalThis[Symbol.for("windmill-lite.bench.calls")]), and its onRunEnd
// hook takes every run's trace: the full trace, or what WL_BENCH_TRACE
// (summary or none) declares. With WL_BENCH_TRACE_KEYS=1, onRunEnd answers
// the keys of the trace it got as a warning (code bench_trace).
//
// Environment: WL_ENGINE (the engine binary the SDK starts), WL_BUNDLE
// (the bundle directory), WL_BENCH_PLUGIN, WL_BENCH_TRACE and
// WL_BENCH_TRACE_KEYS. Its working directory is the one it passes.

import { connect, type Plugin, type TraceDetail, type TraceNone } from "../../../../harnesses/bun/host/index.ts";

const env = process.env;

// The calls of the plugin's provider hooks, by "<run> <path>".
const calls: Record<string, number> = {};
(globalThis as Record<symbol, unknown>)[Symbol.for("windmill-lite.bench.calls")] = calls;

// The synthetic resources. r4 holds a $WM_ string, which the engine
// substitutes for each job after the read, and so does f/bench/jobres;
// f/bench/counter answers how many times the run has read it, so a read
// that is not repeated shows.
const resources: Record<string, { type: string; value: unknown }> = {};
for (let k = 1; k <= 4; k++) {
  const value = { host: `db-${k}.bench.example.invalid`, port: 5430 + k, user: "bench", pool: { min: 1, max: 4 * k } };
  resources[`f/bench/r${k}`] = { type: "c_bench_db", value: k === 4 ? { ...value, workspace: "$WM_WORKSPACE" } : value };
  resources[`f/bench/slow/r${k}`] = resources[`f/bench/r${k}`];
}
resources["f/bench/jobres"] = { type: "c_bench_job", value: { job: "$WM_JOB_ID" } };

function count(run: string, path: string) {
  const key = `${run} ${path}`;
  calls[key] = (calls[key] ?? 0) + 1;
}

const plugin: Plugin = {
  resource: async (path, e) => {
    count(e.run, path);
    if (path.startsWith("f/bench/slow/")) await Bun.sleep(1);
    if (path === "f/bench/counter") return { type: "c_bench_counter", value: { seen: calls[`${e.run} ${path}`] } };
    return resources[path];
  },
  variable: async (path, e) => {
    count(e.run, path);
    return path === "f/bench/v1" ? { value: "synthetic-setting" } : undefined;
  },
  // Takes every run's trace, as a host that reports runs does.
  onRunEnd: (t: TraceNone) => {
    if (env.WL_BENCH_TRACE_KEYS === "1") return [{ code: "bench_trace", message: Object.keys(t).sort().join(",") }];
    if (t.status === "") throw new Error("unreachable");
  },
  traceDetail: env.WL_BENCH_TRACE ? { onRunEnd: env.WL_BENCH_TRACE as TraceDetail } : undefined,
};

await connect({
  engine: env.WL_ENGINE!,
  bundle: env.WL_BUNDLE!,
  workdir: process.cwd(),
  plugin: env.WL_BENCH_PLUGIN === "1" ? plugin : undefined,
  // The engine is not this host's child here, so it runs no watchdog.
  watchdog: { killAfter: 0 },
  // The benchmark closes the engine when it is done: then the host ends.
  onEngineExit: () => process.exit(0),
});
console.log(`ready ${process.pid}`);
