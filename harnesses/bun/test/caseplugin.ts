// The host plugin of the plugin cases (conformance/plugins/hooks.yaml,
// milestone F2c): one plugin that answers every case's declarative plugin,
// each for the sites of its own flow, which the Go tests load in warm
// harness processes (wl, wl.Options.Plugin) and in a compiled host
// (cmd/windmill-lite, the host SDK's plugin option). It keeps a log of
// each run, which onRunEnd reports in the warning plugin_log. Its
// onRunSettled appends a line to the report file, when it has one: the
// run's id, and whether one of the run's hooks still ran then, which must
// never be so (ADR 0008 H9: a run's resources outlive its hooks). A file,
// because each warm harness process loads a copy of the plugin of its own.

import { appendFileSync } from "node:fs";
import type { Plugin, Selector } from "../runtime/plugin.ts";

type Answer = { result?: unknown; failed?: unknown; decline?: boolean; throw?: string; delay_ms?: number; static?: unknown };

/** How an H13 provider fails for a path: it throws, or answers after
 * delay_ms, beyond the plugin timeout, with nothing. */
type Failing = { throw?: string; delay_ms?: number };

export type CaseSpec = {
  intercept?: ({ module: string } & Answer)[];
  rewrite_args?: { module: string; args: Record<string, unknown> }[];
  halt_before?: string[];
  halt_after?: string[];
  write_capable?: string[];
  first_write?: string;
  fatal_error_names?: string[];
  around?: { module: string; throw?: "before" | "after" }[];
  observe?: boolean;
  env?: Record<string, string>;
  variables?: Record<string, string>;
  fail_variables?: Record<string, Failing>;
  fail_resources?: Record<string, Failing>;
  slow_start_ms?: number;
  providers?: Record<string, Answer>;
  warnings?: { code: string; message: string }[];
};

/** cases: each case's flow path and declarative plugin; report: the file
 * onRunSettled appends `{"run", "early"}` lines to. */
export function casePlugin(cases: { flow: string; plugin: CaseSpec }[], report?: string): Plugin {
  const logs = new Map<string, string[]>();
  const runFlow = new Map<string, string>();
  // The hooks of each run still running, which its settlement must wait for.
  const active = new Map<string, number>();
  const busy = async <T>(run: string, f: () => T | Promise<T>): Promise<T> => {
    active.set(run, (active.get(run) ?? 0) + 1);
    try {
      return await f();
    } finally {
      const n = active.get(run)! - 1;
      if (n > 0) active.set(run, n);
      else active.delete(run);
    }
  };
  const fail = async (f: Failing | undefined) => {
    if (!f) return false;
    if (f.delay_ms) await Bun.sleep(f.delay_ms);
    if (f.throw !== undefined) throw new Error(f.throw);
    return true;
  };
  const byFlow = new Map(cases.map((c) => [c.flow, c.plugin]));
  const log = (run: string, entry: string) => {
    let l = logs.get(run);
    if (!l) logs.set(run, (l = []));
    l.push(entry);
  };
  const answer = async (a: Answer) => {
    if (a.delay_ms) await Bun.sleep(a.delay_ms);
    if (a.throw !== undefined) throw new Error(a.throw);
    if (a.decline) return undefined;
    if (a.failed !== undefined) return { result: a.failed, failed: true };
    return { result: a.result ?? null };
  };
  const p: Required<Pick<Plugin, "intercept" | "rewriteArgs" | "haltBefore" | "haltAfter" | "writeCapable" | "fatalErrorNames" | "around" | "providers">> = {
    intercept: [],
    rewriteArgs: [],
    haltBefore: [],
    haltAfter: [],
    writeCapable: [],
    fatalErrorNames: [],
    around: [],
    providers: {},
  };
  for (const c of cases) {
    const sel = (module: string): Selector => ({ flow: c.flow, module });
    const s = c.plugin;
    for (const x of s.intercept ?? []) {
      if (x.static !== undefined) p.intercept.push({ selector: sel(x.module), static: x.static });
      else p.intercept.push({ selector: sel(x.module), fn: () => answer(x) });
    }
    for (const x of s.rewrite_args ?? []) p.rewriteArgs.push({ selector: sel(x.module), fn: () => x.args });
    for (const m of s.halt_before ?? []) p.haltBefore.push(sel(m));
    for (const m of s.halt_after ?? []) p.haltAfter.push(sel(m));
    for (const m of s.write_capable ?? []) p.writeCapable.push(sel(m));
    p.fatalErrorNames.push(...(s.fatal_error_names ?? []));
    for (const x of s.around ?? []) {
      p.around.push({
        selector: sel(x.module),
        fn: async (call, next) => {
          log(call.run, `around-before:${call.site}`);
          if (x.throw === "before") throw new Error("the around wrapper failed before the step");
          const o = await next();
          log(call.run, `around-after:${call.site}:${o.outcome}`);
          if (x.throw === "after") throw new Error("the around wrapper failed after the step");
        },
      });
    }
    for (const [name, a] of Object.entries(s.providers ?? {})) p.providers[name] = () => answer(a);
  }
  return {
    ...p,
    beforeFirstWrite: ({ site }) => {
      const w = byFlow.get(site.flow)?.first_write ?? "approve";
      if (w.startsWith("veto: ")) throw new Error(w.slice("veto: ".length));
    },
    onModuleStart: ({ run, site }) =>
      busy(run, async () => {
        runFlow.set(run, site.flow);
        const s = byFlow.get(site.flow);
        if (s?.observe) log(run, `start:${site.path}`);
        if (s?.slow_start_ms) await Bun.sleep(s.slow_start_ms);
      }),
    onModuleEnd: ({ run, site, record }) => {
      if (byFlow.get(site.flow)?.observe) log(run, `end:${site.path}:${record.status}`);
    },
    invocationEnv: ({ site, env }) => {
      Object.assign(env, byFlow.get(site.flow)?.env ?? {});
    },
    onRunEnd: (t) => {
      const ws = [
        { code: "plugin_log", message: JSON.stringify(logs.get(t.run) ?? []) },
        ...(byFlow.get(runFlow.get(t.run) ?? "")?.warnings ?? []),
      ];
      logs.delete(t.run);
      return ws;
    },
    onRunSettled: (t) => {
      if (report) appendFileSync(report, JSON.stringify({ run: t.run, early: active.has(t.run) }) + "\n");
    },
    variable: (path, { run }) =>
      busy(run, async () => {
        const s = byFlow.get(runFlow.get(run) ?? "");
        if (await fail(s?.fail_variables?.[path])) return undefined;
        const v = s?.variables?.[path];
        return v === undefined ? undefined : { value: v };
      }),
    resource: (path, { run }) =>
      busy(run, async () => {
        await fail(byFlow.get(runFlow.get(run) ?? "")?.fail_resources?.[path]);
        return undefined;
      }),
  };
}
