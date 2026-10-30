// Developer checks of host plugins (runtime/plugin.ts, milestone F2c): the
// declaration, the dispatch of each hook, and the runtime answering
// host_call frames. The gate runs in Go: wl (warm) and cmd/windmill-lite
// (host mode) run conformance/plugins/hooks.yaml through the real harness.

import { AsyncLocalStorage } from "node:async_hooks";
import { describe, expect, test } from "bun:test";
import { decodeAll, encode, FrameReader, frame } from "../runtime/frame.ts";
import { declaration, dispatch, HostCallFailure, type Plugin } from "../runtime/plugin.ts";
import { Runtime } from "../runtime/runtime.ts";

const site = { path: "m:a", flow: "f/x", module: "a", script: "f/x/a" };

describe("declaration", () => {
  test("names the hooks a plugin answers, with their selectors and static values", () => {
    const p: Plugin = {
      intercept: [{ selector: { flow: "f/x", module: "a", extra: 1 } as never, static: { s: 1 } }, { selector: { script: "f/x/b" }, fn: () => undefined }],
      haltAfter: [{ flow: "f/x", module: "c" }],
      around: [{ selector: { flow: "f/x", module: "a" }, fn: (_c, next) => next() }],
      onRunEnd: () => [],
      providers: { zeta: () => undefined, alpha: () => undefined },
      variable: () => undefined,
    };
    expect(declaration(p)).toEqual({
      intercept: [{ selector: { flow: "f/x", module: "a" }, static: { s: 1 } }, { selector: { script: "f/x/b" } }],
      halt_after: [{ flow: "f/x", module: "c" }],
      around: [{ selector: { flow: "f/x", module: "a" } }],
      on_run_end: true,
      providers: ["alpha", "zeta"],
      variable: true,
    });
  });

  test("refuses an interception with both or neither of static and fn, and a hook that is no function", () => {
    expect(() => declaration({ intercept: [{ selector: { script: "s" }, static: 1, fn: () => undefined }] })).toThrow("exactly one");
    expect(() => declaration({ intercept: [{ selector: { script: "s" } }] })).toThrow("exactly one");
    expect(() => declaration({ onModuleStart: 1 as never })).toThrow("not a function");
  });
});

describe("dispatch", () => {
  const p: Plugin = {
    intercept: [{ selector: { script: "s" }, static: 1 }, { selector: { script: "s" }, fn: (c) => (c.args.x === 1 ? { result: c.site.path } : undefined) }],
    rewriteArgs: [{ selector: { script: "s" }, fn: (c) => ({ ...c.args, y: 2 }) }],
    invocationEnv: ({ env }) => {
      env.A = "1";
    },
    providers: { bad: () => ({ nope: 1 }) as never, failing: () => ({ result: { name: "E" }, failed: true }) },
    resource: (path) => (path === "u/r" ? { type: "t", value: { k: 1 } } : undefined),
    onRunEnd: () => {
      throw new Error("down");
    },
  };

  test("answers each hook", async () => {
    expect(await dispatch(p, "intercept", "r", { index: 1, site, args: { x: 1 } })).toEqual({ result: "m:a" });
    expect(await dispatch(p, "intercept", "r", { index: 1, site, args: { x: 2 } })).toEqual({ decline: true });
    expect(await dispatch(p, "rewrite_args", "r", { index: 0, site, args: { x: 1 } })).toEqual({ args: { x: 1, y: 2 } });
    expect(await dispatch(p, "invocation_env", "r", { site, env: { B: "2" } })).toEqual({ env: { B: "2", A: "1" } });
    expect(await dispatch(p, "provide", "r", { name: "failing", site, args: {} })).toEqual({ result: { name: "E" }, failed: true });
    expect(await dispatch(p, "resource", "r", { path: "u/r" })).toEqual({ found: true, type: "t", value: { k: 1 } });
    expect(await dispatch(p, "resource", "r", { path: "u/none" })).toEqual({ found: false });
  });

  const code = async (hook: string, payload: Record<string, unknown>) => {
    try {
      await dispatch(p, hook, "r", payload);
    } catch (e) {
      return e instanceof HostCallFailure ? e.code : String(e);
    }
    return "answered";
  };

  test("refuses what the plugin does not declare (unknown_hook) and reports what fails (plugin_error)", async () => {
    expect(await code("variable", { path: "v" })).toBe("unknown_hook");
    expect(await code("intercept", { index: 9, site, args: {} })).toBe("unknown_hook");
    expect(await code("intercept", { index: 0, site, args: {} })).toBe("unknown_hook");
    expect(await code("provide", { name: "none", site, args: {} })).toBe("unknown_hook");
    expect(await code("no_such_hook", {})).toBe("unknown_hook");
    expect(await code("on_run_end", { trace: {} })).toBe("plugin_error");
    expect(await code("provide", { name: "bad", site, args: {} })).toBe("plugin_error");
  });
});

/** A host-mode runtime whose frames the test reads. */
function hostRuntime(plugin?: Plugin) {
  const written: Uint8Array[] = [];
  const runtime = new Runtime({ bundleDir: "/nonexistent", bundleDigest: "sha256:b", host: true, plugin, transport: { write: (b) => (written.push(b.slice()), b.length), end: () => {} }, exit: () => {} });
  runtime.start();
  const send = (m: Record<string, unknown>) => runtime.onData(frame(encode(m as { t: string })));
  send({ t: "welcome", proto: 1, engine: "test", bundle: "sha256:b", config: { module_state: "warm", heartbeat_ms: 60000, max_message: 1 << 20 } });
  const messages = () => decodeAll(new FrameReader().push(Buffer.concat(written)));
  return { runtime, send, messages };
}

describe("the runtime", () => {
  test("its hello carries the plugin's declaration", () => {
    const { messages, runtime } = hostRuntime({ onModuleStart: () => {} });
    expect(messages()[0].plugin).toEqual({ on_module_start: true });
    runtime.onClose();
  });

  test("answers a host_call in the scope of the run it serves (H11)", async () => {
    const store = new AsyncLocalStorage<string>();
    let seen: string | undefined = "unset";
    const { runtime, send, messages } = hostRuntime({
      onModuleStart: () => {
        seen = store.getStore();
      },
    });
    const run = store.run("tenant-1", () => runtime.submit("flow", "f/x", "{}"));
    run.done.catch(() => {}); // the connection ends with the test
    send({ t: "host_call", id: "hc1", hook: "on_module_start", run: run.id, site: "m:a", payload: { site } });
    await Bun.sleep(5);
    expect(seen).toBe("tenant-1");
    expect(messages().find((m) => m.t === "host_reply")).toEqual({ t: "host_reply", id: "hc1", payload: {} });
    runtime.onClose();
  });

  test("answers unknown_hook when it loaded no plugin, and plugin_error when the hook throws", async () => {
    const plain = hostRuntime();
    plain.send({ t: "host_call", id: "hc1", hook: "intercept", run: "r", site: "", payload: {} });
    await Bun.sleep(5);
    expect(plain.messages().find((m) => m.t === "host_reply")?.error?.code).toBe("unknown_hook");
    plain.runtime.onClose();
    const failing = hostRuntime({
      onModuleStart: () => {
        throw new Error("observer down");
      },
    });
    failing.send({ t: "host_call", id: "hc2", hook: "on_module_start", run: "r", site: "m:a", payload: { site } });
    await Bun.sleep(5);
    const reply = failing.messages().find((m) => m.t === "host_reply");
    expect(reply?.error?.code).toBe("plugin_error");
    expect(reply?.error?.message).toContain("observer down");
    failing.runtime.onClose();
  });
});
