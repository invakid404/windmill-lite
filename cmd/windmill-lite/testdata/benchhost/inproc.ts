// The in-process side of the embedder shapes (cmd/windmill-lite/
// bench_embed_test.go): the same step functions a workload's flow runs,
// composed by hand in one Bun process as plain function calls, with the
// same store and API reads. Each workload is a generated ES module whose
// default export runs one request (embWorkload.program).
//
// It reads one JSON command per line on its standard input and answers each
// with one JSON line on file descriptor 3 (its standard output takes the
// steps' console output):
//
//   {"op":"load","name":N,"file":F}   imports F as workload N;
//   {"op":"run","name":N,"conc":C,"args":A}
//                                     runs C requests of N at once with flow
//                                     input A, and answers their elapsed
//                                     times (ns), the batch's CPU time (µs,
//                                     user and system) and the first result.
//
// Environment: WL_BENCH_STORE_URL (the store's origin) and
// WL_BENCH_EMB_API_US (the API's delay per resource read).

import { writeSync } from "node:fs";

const storeURL = process.env.WL_BENCH_STORE_URL!;
const apiDelay = process.env.WL_BENCH_EMB_API_US ?? "0";

type Program = (flowInput: unknown, rt: Rt) => Promise<unknown>;
type Rt = {
  res: (cache: Map<string, Promise<unknown>>, path: string) => Promise<unknown>;
  parallel: <T>(items: unknown[], limit: number, f: (value: unknown, index: number) => Promise<T>) => Promise<T[]>;
};

// A request reads each resource from the API once, as the run's read cache
// does on the other side.
const rt: Rt = {
  res(cache, path) {
    let v = cache.get(path);
    if (v === undefined) {
      v = fetch(`${storeURL}/res/${path}?d=${apiDelay}`).then(async (r) => ((await r.json()) as { value: unknown }).value);
      cache.set(path, v);
    }
    return v;
  },
  // A parallel loop: limit iterations at once, results in item order.
  async parallel(items, limit, f) {
    const out = new Array(items.length);
    let next = 0;
    const worker = async () => {
      while (next < items.length) {
        const i = next++;
        out[i] = await f(items[i], i);
      }
    };
    await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
    return out;
  },
};

const programs = new Map<string, Program>();

function reply(v: unknown) {
  writeSync(3, JSON.stringify(v) + "\n");
}

let buf = "";
const decoder = new TextDecoder();
for await (const chunk of Bun.stdin.stream()) {
  buf += decoder.decode(chunk, { stream: true });
  let nl: number;
  while ((nl = buf.indexOf("\n")) >= 0) {
    const line = buf.slice(0, nl);
    buf = buf.slice(nl + 1);
    if (line.trim() === "") continue;
    const m = JSON.parse(line);
    try {
      if (m.op === "load") {
        programs.set(m.name, (await import(m.file)).default);
        reply({});
      } else if (m.op === "run") {
        const prog = programs.get(m.name)!;
        const c0 = process.cpuUsage();
        const runs = await Promise.all(
          Array.from({ length: m.conc }, async () => {
            const t0 = Bun.nanoseconds();
            const out = await prog(m.args, rt);
            return { ns: Bun.nanoseconds() - t0, out };
          }),
        );
        const c = process.cpuUsage(c0);
        reply({ elapsed: runs.map((r) => r.ns), cpu: c.user + c.system, result: runs[0].out ?? null });
      } else {
        reply({ error: `unknown op ${m.op}` });
      }
    } catch (e) {
      reply({ error: String((e as Error)?.stack ?? e) });
    }
  }
}
