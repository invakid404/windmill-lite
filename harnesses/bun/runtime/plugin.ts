// Host plugins (ADR 0008, milestone F2c): TypeScript hooks that the
// engine calls over host_call frames, answered here, in the harness. A
// plugin declares its hooks and their selectors up front; the harness
// sends that declaration in its hello, and the engine builds its Go hook
// seams from it (F2a), asking the plugin only for the sites the selectors
// match. Selector matching stays in Go, written once: a host_call names
// the declared entry it is for by its index.
//
// In host mode the plugin is an object the host passes to the host SDK's
// connect; in a warm harness the engine spawned, it is the default export
// of a module the engine names (wl.Options.Plugin, `--plugin`).

/** A selector (ADR 0008): a flow's module, with an optional occurrence,
 * iterations or branches, or a script. The form of the host contract's
 * selectors (ir.Selector). */
export type Selector = {
  flow?: string;
  module?: string;
  occurrence?: number;
  iterations?: number[];
  branches?: string[];
  script?: string;
};

/** A call site as a hook sees it: its typed path (ADR 0011), the flow
 * whose module list holds it, the module's id, and a leaf's script path. */
export type Site = { path: string; flow: string; module: string; script: string };

/** What an interception, an argument rewrite or a provider gets. */
export type Call = { run: string; site: Site; args: Record<string, unknown> };

/** What an interception or a provider answers in place of the job: its
 * result, or, with failed, its error value (H1). */
export type Outcome = { result: unknown; failed?: boolean };

/** How a step's invocation ended, as an around wrapper's next() gives it:
 * the harness's outcome, and for ok and error the result or error value. */
export type StepOutcome = { outcome: "ok" | "error" | "escaped" | "empty"; result?: unknown };

/** What an around wrapper gets: the invocation's run, site path, script,
 * arguments and environment. */
export type AroundCall = { run: string; site: string; script: string; args: Record<string, unknown>; env: Record<string, string> };

/** A site's record in the trace (H9), as the hooks see it. */
export type StepRecord = {
  site: string;
  attempt: number;
  job?: string;
  status: string;
  module?: string;
  ran: boolean;
  skipped: boolean;
  abandoned: boolean;
  cached: boolean;
  hooks: string[];
  args?: unknown;
  result?: unknown;
};

/** A run's trace (H9), as onRunEnd and onRunSettled see it. */
export type Trace = {
  run: string;
  status: string;
  result?: unknown;
  may_have_mutated: boolean;
  latched?: string;
  steps: StepRecord[];
  warnings: Warning[];
};

export type Warning = { code: string; message: string };

type MaybePromise<T> = T | Promise<T>;

export type Plugin = {
  /** H1: the first interception whose selector matches a site answers for
   * its job: a static value, or fn's outcome; undefined declines. */
  intercept?: { selector: Selector; static?: unknown; fn?: (c: Call) => MaybePromise<Outcome | undefined> }[];
  /** H2: every matching rewrite, in order, returns the arguments. */
  rewriteArgs?: { selector: Selector; fn: (c: Call) => MaybePromise<Record<string, unknown>> }[];
  /** H8: halts before or after the modules the selectors match. */
  haltBefore?: Selector[];
  haltAfter?: Selector[];
  /** H3: the write-capable sites; before the first of them launches in a
   * run, beforeFirstWrite is called once, and a throw vetoes the write,
   * which latches the run. */
  writeCapable?: Selector[];
  beforeFirstWrite?: (e: { run: string; site: Site }) => MaybePromise<void>;
  /** H7: error names that latch the run instead of reaching retry,
   * continue_on_error, skip_failures or skip_failure. */
  fatalErrorNames?: string[];
  /** H1's wrap: every matching wrapper runs around a leaf's invocation,
   * here, outermost first. It calls next() once to run the step; the
   * step's outcome stays its own, whatever the wrapper returns. A wrapper
   * that throws latches the run. */
  around?: { selector: Selector; fn: (c: AroundCall, next: () => Promise<StepOutcome>) => MaybePromise<unknown> }[];
  /** H6: observers of every module. */
  onModuleStart?: (e: { run: string; site: Site }) => MaybePromise<void>;
  onModuleEnd?: (e: { run: string; site: Site; record: StepRecord }) => MaybePromise<void>;
  /** H12: returns the invocation's environment, or changes e.env and
   * returns nothing. */
  invocationEnv?: (e: { run: string; site: Site; env: Record<string, string> }) => MaybePromise<Record<string, string> | void>;
  /** H9: warnings to append to the run's outcome. */
  onRunEnd?: (t: Trace) => MaybePromise<Warning[] | void>;
  onRunSettled?: (t: Trace) => MaybePromise<void>;
  /** The named providers of mandatory host-provided sites (ADR 0002, ADR
   * 0008): an outcome, or undefined, which declines and fails the run
   * closed (provider_declined). */
  providers?: Record<string, (c: Call) => MaybePromise<Outcome | undefined>>;
  /** H13, the provider bridge (ADR 0007): the raw value of a resource or
   * a variable for a run, or undefined for none, when the run's own and
   * then the runtime's answer otherwise. The engine's router does
   * interpolation, errors and the 501 policy. */
  resource?: (path: string, e: { run: string }) => MaybePromise<{ type: string; value: unknown } | undefined>;
  variable?: (path: string, e: { run: string }) => MaybePromise<{ value: string; secret?: boolean } | undefined>;
};

/** A plugin's failure to answer a host_call: code is plugin_error or
 * unknown_hook. */
export class HostCallFailure extends Error {
  constructor(
    readonly code: "plugin_error" | "unknown_hook",
    message: string,
  ) {
    super(message);
  }
}

const SELECTOR_KEYS = ["flow", "module", "occurrence", "iterations", "branches", "script"] as const;

function selector(where: string, s: unknown): Selector {
  if (!s || typeof s !== "object" || Array.isArray(s)) throw new Error(`windmill-lite plugin: ${where}: a selector is an object`);
  const out: Record<string, unknown> = {};
  for (const k of SELECTOR_KEYS) if ((s as Record<string, unknown>)[k] !== undefined) out[k] = (s as Record<string, unknown>)[k];
  return out as Selector;
}

function fn(where: string, f: unknown) {
  if (typeof f !== "function") throw new Error(`windmill-lite plugin: ${where} is not a function`);
}

/** The plugin's declaration, the hello's plugin field: the hooks it
 * answers and their selectors (static interceptions with their values).
 * It throws on a plugin whose shape is wrong; the engine checks the
 * selectors themselves. */
export function declaration(p: Plugin): Record<string, unknown> {
  if (!p || typeof p !== "object") throw new Error("windmill-lite plugin: a plugin is an object");
  const d: Record<string, unknown> = {};
  if (p.intercept) {
    d.intercept = p.intercept.map((x, i) => {
      const where = `intercept[${i}]`;
      if ((x.fn === undefined) === (x.static === undefined)) throw new Error(`windmill-lite plugin: ${where}: give exactly one of static and fn`);
      if (x.fn !== undefined) fn(`${where}.fn`, x.fn);
      return x.fn !== undefined ? { selector: selector(where, x.selector) } : { selector: selector(where, x.selector), static: x.static };
    });
  }
  if (p.rewriteArgs) {
    d.rewrite_args = p.rewriteArgs.map((x, i) => {
      fn(`rewriteArgs[${i}].fn`, x.fn);
      return { selector: selector(`rewriteArgs[${i}]`, x.selector) };
    });
  }
  if (p.haltBefore) d.halt_before = p.haltBefore.map((s, i) => selector(`haltBefore[${i}]`, s));
  if (p.haltAfter) d.halt_after = p.haltAfter.map((s, i) => selector(`haltAfter[${i}]`, s));
  if (p.writeCapable) d.write_capable = p.writeCapable.map((s, i) => selector(`writeCapable[${i}]`, s));
  if (p.fatalErrorNames) {
    if (!Array.isArray(p.fatalErrorNames) || p.fatalErrorNames.some((n) => typeof n !== "string")) throw new Error("windmill-lite plugin: fatalErrorNames is a list of names");
    d.fatal_error_names = [...p.fatalErrorNames];
  }
  if (p.around) {
    d.around = p.around.map((x, i) => {
      fn(`around[${i}].fn`, x.fn);
      return { selector: selector(`around[${i}]`, x.selector) };
    });
  }
  for (const [key, name] of [
    ["beforeFirstWrite", "before_first_write"],
    ["onModuleStart", "on_module_start"],
    ["onModuleEnd", "on_module_end"],
    ["invocationEnv", "invocation_env"],
    ["onRunEnd", "on_run_end"],
    ["onRunSettled", "on_run_settled"],
    ["resource", "resource"],
    ["variable", "variable"],
  ] as const) {
    if (p[key] === undefined) continue;
    fn(key, p[key]);
    d[name] = true;
  }
  if (p.providers) {
    const names = Object.keys(p.providers).sort();
    for (const n of names) fn(`providers.${n}`, p.providers[n]);
    d.providers = names;
  }
  return d;
}

/** The outcome an interception or a provider answered, as the reply's
 * payload: decline, or a result with its failed flag. */
function outcome(where: string, o: Outcome | undefined): Record<string, unknown> {
  if (o === undefined) return { decline: true };
  if (!o || typeof o !== "object" || !("result" in o)) throw new HostCallFailure("plugin_error", `${where} answered ${JSON.stringify(o)}, neither undefined nor an outcome with a result`);
  return o.failed ? { result: o.result ?? null, failed: true } : { result: o.result ?? null };
}

function entry<T>(list: T[] | undefined, index: unknown, hook: string): T {
  const e = typeof index === "number" ? list?.[index] : undefined;
  if (e === undefined) throw new HostCallFailure("unknown_hook", `the plugin declares no ${hook} entry ${JSON.stringify(index)}`);
  return e;
}

/** Answers one host_call: the hook's answer as the reply's payload. It
 * throws a HostCallFailure: unknown_hook for a hook the plugin does not
 * declare, plugin_error when the hook throws or answers what it does not
 * answer. */
export async function dispatch(p: Plugin, hook: string, run: string, payload: Record<string, unknown>): Promise<Record<string, unknown>> {
  const site = payload.site as Site;
  const call = (): Call => ({ run, site, args: (payload.args ?? {}) as Record<string, unknown> });
  const unknown = () => new HostCallFailure("unknown_hook", `the plugin declares no ${hook} hook`);
  try {
    switch (hook) {
      case "intercept": {
        const x = entry(p.intercept, payload.index, hook);
        if (!x.fn) throw new HostCallFailure("unknown_hook", `intercept entry ${payload.index} is static`);
        return outcome(`intercept[${payload.index}]`, await x.fn(call()));
      }
      case "rewrite_args": {
        const args = await entry(p.rewriteArgs, payload.index, hook).fn(call());
        if (!args || typeof args !== "object" || Array.isArray(args)) throw new HostCallFailure("plugin_error", `rewriteArgs[${payload.index}] answered arguments that are not an object`);
        return { args };
      }
      case "before_first_write":
        if (!p.beforeFirstWrite) throw unknown();
        await p.beforeFirstWrite({ run, site });
        return {};
      case "on_module_start":
        if (!p.onModuleStart) throw unknown();
        await p.onModuleStart({ run, site });
        return {};
      case "on_module_end":
        if (!p.onModuleEnd) throw unknown();
        await p.onModuleEnd({ run, site, record: payload.record as StepRecord });
        return {};
      case "invocation_env": {
        if (!p.invocationEnv) throw unknown();
        const env = { ...(payload.env as Record<string, string>) };
        const out = await p.invocationEnv({ run, site, env });
        const next = out ?? env;
        if (!next || typeof next !== "object" || Object.values(next).some((v) => typeof v !== "string")) {
          throw new HostCallFailure("plugin_error", "invocationEnv answered an environment whose values are not all strings");
        }
        return { env: next };
      }
      case "on_run_end": {
        if (!p.onRunEnd) throw unknown();
        const ws = (await p.onRunEnd(payload.trace as Trace)) ?? [];
        if (!Array.isArray(ws) || ws.some((w) => !w || typeof w.code !== "string" || typeof w.message !== "string")) {
          throw new HostCallFailure("plugin_error", "onRunEnd answered warnings that are not {code, message} objects");
        }
        return { warnings: ws.map((w) => ({ code: w.code, message: w.message })) };
      }
      case "on_run_settled":
        if (!p.onRunSettled) throw unknown();
        await p.onRunSettled(payload.trace as Trace);
        return {};
      case "provide": {
        const name = String(payload.name);
        const f = p.providers && Object.hasOwn(p.providers, name) ? p.providers[name] : undefined;
        if (!f) throw new HostCallFailure("unknown_hook", `the plugin declares no provider ${JSON.stringify(name)}`);
        return outcome(`providers.${name}`, await f(call()));
      }
      case "resource": {
        if (!p.resource) throw unknown();
        const r = await p.resource(String(payload.path), { run });
        if (r === undefined) return { found: false };
        if (!r || typeof r.type !== "string") throw new HostCallFailure("plugin_error", "resource answered neither undefined nor {type, value}");
        return { found: true, type: r.type, value: r.value ?? null };
      }
      case "variable": {
        if (!p.variable) throw unknown();
        const v = await p.variable(String(payload.path), { run });
        if (v === undefined) return { found: false };
        if (!v || typeof v.value !== "string") throw new HostCallFailure("plugin_error", "variable answered neither undefined nor {value}");
        return { found: true, value: v.value, secret: v.secret === true };
      }
    }
    throw unknown();
  } catch (e) {
    if (e instanceof HostCallFailure) throw e;
    throw new HostCallFailure("plugin_error", e instanceof Error ? `${e.name}: ${e.message}` : String(e));
  }
}

/** Loads a warm harness's plugin: the default export of the module at
 * path, or its export named plugin. */
export async function loadPlugin(path: string): Promise<Plugin> {
  const mod = (await import(path)) as { default?: Plugin; plugin?: Plugin };
  const p = mod.default ?? mod.plugin;
  if (!p || typeof p !== "object") throw new Error(`${path} exports no plugin (a default export, or one named plugin)`);
  declaration(p);
  return p;
}
