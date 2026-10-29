// The host plugin of the plugin cases (conformance/plugins/hooks.yaml,
// milestone F2c): one plugin that answers every case's declarative plugin,
// each for the sites of its own flow, which the Go tests load in warm
// harness processes (wl, wl.Options.Plugin) and in a compiled host
// (cmd/windmill-lite, the host SDK's plugin option). It keeps a log of
// each run, which onRunEnd reports in the warning plugin_log, and the runs
// that settled so far, in plugin_settled.

import type { Plugin, Selector } from "../runtime/plugin.ts";

type Answer = { result?: unknown; failed?: unknown; decline?: boolean; throw?: string; delay_ms?: number; static?: unknown };

export type CaseSpec = {
  intercept?: ({ module: string } & Answer)[];
  rewrite_args?: { module: string; args: Record<string, unknown> }[];
  halt_before?: string[];
  halt_after?: string[];
  around?: { module: string; throw?: "before" | "after" }[];
  observe?: boolean;
  env?: Record<string, string>;
  variables?: Record<string, string>;
  providers?: Record<string, Answer>;
  warnings?: { code: string; message: string }[];
};

/** cases: each case's flow path and declarative plugin. */
export function casePlugin(cases: { flow: string; plugin: CaseSpec }[]): Plugin {
  const logs = new Map<string, string[]>();
  const runFlow = new Map<string, string>();
  const settled: string[] = [];
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
  const p: Required<Pick<Plugin, "intercept" | "rewriteArgs" | "haltBefore" | "haltAfter" | "around" | "providers">> = {
    intercept: [],
    rewriteArgs: [],
    haltBefore: [],
    haltAfter: [],
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
    onModuleStart: ({ run, site }) => {
      runFlow.set(run, site.flow);
      if (byFlow.get(site.flow)?.observe) log(run, `start:${site.path}`);
    },
    onModuleEnd: ({ run, site, record }) => {
      if (byFlow.get(site.flow)?.observe) log(run, `end:${site.path}:${record.status}`);
    },
    invocationEnv: ({ site, env }) => {
      Object.assign(env, byFlow.get(site.flow)?.env ?? {});
    },
    onRunEnd: (t) => {
      const ws = [
        { code: "plugin_log", message: JSON.stringify(logs.get(t.run) ?? []) },
        { code: "plugin_settled", message: JSON.stringify(settled) },
        ...(byFlow.get(runFlow.get(t.run) ?? "")?.warnings ?? []),
      ];
      logs.delete(t.run);
      return ws;
    },
    onRunSettled: (t) => {
      settled.push(t.run);
    },
    variable: (path, { run }) => {
      const v = byFlow.get(runFlow.get(run) ?? "")?.variables?.[path];
      return v === undefined ? undefined : { value: v };
    },
  };
}
