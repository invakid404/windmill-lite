// Developer checks of host plugins (runtime/plugin.ts): the
// declaration, the dispatch of each hook, and the runtime answering
// host_call frames. The gate runs in Go: wl (warm) and cmd/windmill-lite
// (host mode) run conformance/plugins/hooks.yaml through the real harness.

import { AsyncLocalStorage } from "node:async_hooks";
import { describe, expect, test } from "bun:test";
import { decodeAll, encode, FrameReader, frame, Raw } from "../runtime/frame.ts";
import { declaration, dispatch, HostCallFailure, type Plugin, replyText } from "../runtime/plugin.ts";
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

  test("declares the run-end trace each hook takes, leaving out the default", () => {
    const hooks = { onRunEnd: () => [], onRunSettled: () => undefined };
    expect(declaration({ ...hooks, traceDetail: { onRunEnd: "summary", onRunSettled: "none" } })).toEqual({ on_run_end: true, on_run_settled: true, trace_detail: { on_run_end: "summary", on_run_settled: "none" } });
    expect(declaration({ ...hooks, traceDetail: { onRunEnd: "full" } })).toEqual({ on_run_end: true, on_run_settled: true });
    expect(declaration({ ...hooks, traceDetail: {} })).toEqual(declaration(hooks));
    expect(() => declaration({ ...hooks, traceDetail: { onRunEnd: "some" as never } })).toThrow('"full", "summary" or "none"');
    expect(() => declaration({ ...hooks, traceDetail: { onModuleEnd: "none" } as never })).toThrow("not a run-end hook");
    expect(() => declaration({ onRunEnd: () => [], traceDetail: { onRunSettled: "none" } })).toThrow("without onRunSettled");
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

  // What onRunEnd returns becomes the reply's payload, whose text the
  // runtime sends as replyText writes it.
  const runEnd = async (f: NonNullable<Plugin["onRunEnd"]>) => replyText(await dispatch({ onRunEnd: f }, "on_run_end", "r", { trace: { run: "r", status: "success" } }));
  const w = { code: "c", message: "m" };

  test("onRunEnd's warnings, or nothing, answer warnings alone, with no answer", async () => {
    for (const f of [() => [w], async () => [w]]) {
      expect(await runEnd(f)).toBe('{"warnings":[{"code":"c","message":"m"}]}');
    }
    for (const f of [() => undefined, async () => {}, () => null as never, () => []]) {
      expect(await runEnd(f)).toBe('{"warnings":[]}');
    }
  });

  test("onRunEnd's { warnings?, answer? } answers the warnings and the answer, null included", async () => {
    const answer = { outcome: "retry", after_ms: 250, notes: ["a", null] };
    expect(await runEnd(() => ({ warnings: [w], answer }))).toBe(
      '{"warnings":[{"code":"c","message":"m"}],"answer":{"outcome":"retry","after_ms":250,"notes":["a",null]}}',
    );
    expect(await runEnd(async () => ({ answer: 0 }))).toBe('{"warnings":[],"answer":0}');
    expect(await runEnd(() => ({ answer: "é" }))).toBe('{"warnings":[],"answer":"é"}');
    // null is an answer; an absent or undefined answer is none.
    expect(await runEnd(() => ({ answer: null }))).toBe('{"warnings":[],"answer":null}');
    expect(await runEnd(() => ({ warnings: [w] }))).toBe('{"warnings":[{"code":"c","message":"m"}]}');
    expect(await runEnd(() => ({ answer: undefined }))).toBe('{"warnings":[]}');
    // An object without a prototype is a RunEnd too.
    expect(await runEnd(() => Object.assign(Object.create(null), { answer: 1 }))).toBe('{"warnings":[],"answer":1}');
  });

  test("onRunEnd's answer is read once and serialised once: the reply carries the text that was checked", async () => {
    let reads = 0;
    let serialised = 0;
    const value = { toJSON: () => ({ serialised: ++serialised }) };
    const out = {
      warnings: [w],
      get answer() {
        return ++reads === 1 ? value : undefined;
      },
    };
    expect(await runEnd(() => out)).toBe('{"warnings":[{"code":"c","message":"m"}],"answer":{"serialised":1}}');
    expect([reads, serialised]).toEqual([1, 1]);
  });

  test("a reply too large for max_message goes without its answer, with its size in UTF-8 bytes", async () => {
    const payload = await dispatch({ onRunEnd: () => ({ warnings: [w], answer: "é".repeat(3) }) }, "on_run_end", "r", { trace: {} });
    expect(replyText(payload, "size")).toBe('{"warnings":[{"code":"c","message":"m"}],"answer_bytes":8}');
    expect(replyText(payload, "dropped")).toBe('{"dropped":[{"code":"c","message":"m"}]}');
  });

  test("onRunEnd's answer that JSON cannot carry, a value outside the forms, or bad warnings answer plugin_error", async () => {
    const fails = async (f: () => unknown) => {
      try {
        await runEnd(f as never);
      } catch (e) {
        return e instanceof HostCallFailure ? `${e.code}: ${e.message}` : `not a HostCallFailure: ${e}`;
      }
      return "answered";
    };
    // A value outside the RunEnd form keeps the refusal of warnings that
    // are not {code, message} objects.
    const refused = "plugin_error: onRunEnd answered warnings that are not {code, message} objects";
    class Answer {
      answer = 1;
    }
    for (const f of [() => ({ answers: 1 }), () => ({ warnings: [w], extra: 1 }), () => ({}), () => new Map(), () => new Date(), () => new Answer()]) {
      expect(await fails(f)).toBe(refused);
    }
    expect(await fails(() => ({ answer: () => 1 }))).toBe("plugin_error: onRunEnd answered an answer that JSON cannot encode");
    expect(await fails(() => ({ answer: Symbol("s") }))).toBe("plugin_error: onRunEnd answered an answer that JSON cannot encode");
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(await fails(() => ({ answer: cyclic }))).toMatch(/^plugin_error: TypeError: /);
    expect(await fails(() => ({ warnings: [{ code: 1 }], answer: 1 }))).toBe("plugin_error: onRunEnd answered warnings that are not {code, message} objects");
    expect(await fails(() => ({ warnings: "w", answer: 1 }))).toBe("plugin_error: onRunEnd answered warnings that are not {code, message} objects");
    expect(await fails(() => "w")).toBe("plugin_error: onRunEnd answered warnings that are not {code, message} objects");
  });
});

/** A host-mode runtime whose frames the test reads, welcomed with the
 * message limit maxMessage. */
function hostRuntime(plugin?: Plugin, maxMessage = 1 << 20) {
  const written: Uint8Array[] = [];
  const runtime = new Runtime({ bundleDir: "/nonexistent", bundleDigest: "sha256:b", host: true, plugin, transport: { write: (b) => (written.push(b.slice()), b.length), end: () => {} }, exit: () => {} });
  runtime.start();
  const send = (m: Record<string, unknown>) => runtime.onData(frame(encode(m as { t: string })));
  // A frame as a peer may write it, not through the canonical encoder.
  const sendRaw = (text: string) => runtime.onData(frame(new TextEncoder().encode(text)));
  send({ t: "welcome", proto: 1, engine: "test", bundle: "sha256:b", config: { module_state: "warm", heartbeat_ms: 60000, max_message: maxMessage } });
  const messages = () => decodeAll(new FrameReader().push(Buffer.concat(written)));
  return { runtime, send, sendRaw, messages };
}

/** What plugin code reads as wl.run.data here. */
const readRunData = () => (globalThis as { wl?: { run: { data: unknown } } }).wl!.run.data;

/** Sends a host_call of on_module_start for run, at module, carrying the
 * run_data_id and run_data given. */
function runDataCall(send: (m: Record<string, unknown>) => void) {
  return (id: string, run: string, dataId?: string, data?: unknown, module = "a") =>
    send({ t: "host_call", id, hook: "on_module_start", run, site: "m:" + module, payload: { site: { ...site, path: "m:" + module, module } }, run_data_id: dataId, run_data: data });
}

describe("the runtime", () => {
  test("answers on_run_end with the answer's text, read and serialised once", async () => {
    let reads = 0;
    let serialised = 0;
    const value = { toJSON: () => ({ serialised: ++serialised }) };
    const { runtime, send, messages } = hostRuntime({
      onRunEnd: () => ({
        get answer() {
          return ++reads === 1 ? value : undefined;
        },
      }),
    });
    send({ t: "host_call", id: "hc1", hook: "on_run_end", run: "r", site: "", payload: { trace: { run: "r", status: "success" } } });
    await Bun.sleep(5);
    expect(messages().find((m) => m.t === "host_reply")).toEqual({ t: "host_reply", id: "hc1", payload: { warnings: [], answer: { serialised: 1 } } });
    expect([reads, serialised]).toEqual([1, 1]);
    runtime.onClose();
  });

  test("a run-end reply too large for max_message keeps its warnings and gives its answer's size; one too large without it is an error", async () => {
    const w = { code: "c", message: "m" };
    const { runtime, send, messages } = hostRuntime(
      {
        onRunEnd: (t) => (t.run === "small" ? { warnings: [w], answer: "é".repeat(3000) } : { warnings: [{ code: "c", message: "w".repeat(5000) }], answer: 1 }),
      },
      4096,
    );
    send({ t: "host_call", id: "hc1", hook: "on_run_end", run: "small", site: "", payload: { trace: { run: "small", status: "success" } } });
    send({ t: "host_call", id: "hc2", hook: "on_run_end", run: "large", site: "", payload: { trace: { run: "large", status: "success" } } });
    await Bun.sleep(5);
    const replies = messages().filter((m) => m.t === "host_reply");
    expect(replies.find((m) => m.id === "hc1")).toEqual({ t: "host_reply", id: "hc1", payload: { warnings: [w], answer_bytes: 6002 } });
    const large = replies.find((m) => m.id === "hc2");
    expect(large?.error?.code).toBe("plugin_error");
    expect(large?.error?.message).toContain("the answer cannot be sent");
    runtime.onClose();
  });

  test("a run-end reply whose size does not fit beside its warnings sends them as dropped, which fits whenever they would alone", async () => {
    const frameOf = (payload: string) => encode({ t: "host_reply", id: "hc1", payload: new Raw(payload) }).length;
    const sent = async (w: { code: string; message: string }) => {
      const { runtime, send, messages } = hostRuntime({ onRunEnd: () => ({ warnings: [w], answer: "x".repeat(1048576) }) }, 4096);
      send({ t: "host_call", id: "hc1", hook: "on_run_end", run: "r", site: "", payload: { trace: { run: "r", status: "success" } } });
      await Bun.sleep(5);
      const replies = messages().filter((m) => m.t === "host_reply");
      runtime.onClose();
      return replies;
    };
    // One warning of 4,010 characters: the reply with it alone fits 4,096
    // bytes, and the same reply with answer_bytes does not.
    const w = { code: "c", message: "w".repeat(4010) };
    expect(frameOf(JSON.stringify({ warnings: [w] }))).toBeLessThanOrEqual(4096);
    expect(frameOf(JSON.stringify({ warnings: [w], answer_bytes: 1048578 }))).toBeGreaterThan(4096);
    expect(await sent(w)).toEqual([{ t: "host_reply", id: "hc1", payload: { dropped: [w] } }]);
    // At the edge: under dropped the reply is exactly 4,096 bytes, and under
    // warnings it would be one byte over.
    const edge = { code: "c", message: "w".repeat(4096 - frameOf(JSON.stringify({ dropped: [{ code: "c", message: "" }] }))) };
    expect(frameOf(JSON.stringify({ dropped: [edge] }))).toBe(4096);
    expect(frameOf(JSON.stringify({ warnings: [edge] }))).toBe(4097);
    expect(await sent(edge)).toEqual([{ t: "host_reply", id: "hc1", payload: { dropped: [edge] } }]);
  });

  test("a run-end reply that cannot be written goes as one error reply, whichever form fails", async () => {
    // Warnings whose toJSON throws on every call; only after a first call
    // that wrote a reply too large for max_message; or only on the first
    // call, which a smaller form must not paper over: only a reply too
    // large for max_message tries the next form.
    const brittle = (throws: (call: number) => boolean) => {
      let calls = 0;
      class Brittle extends Array<{ code: string; message: string }> {
        toJSON() {
          if (throws(++calls)) throw new Error("the warnings cannot be written");
          return [...this];
        }
      }
      return Brittle.from([{ code: "c", message: "m" }]);
    };
    for (const throws of [(_: number) => true, (call: number) => call >= 2, (call: number) => call === 1]) {
      const { runtime, send, messages } = hostRuntime({ onRunEnd: () => ({ warnings: brittle(throws), answer: "x".repeat(10000) }) }, 4096);
      send({ t: "host_call", id: "hc1", hook: "on_run_end", run: "r", site: "", payload: { trace: { run: "r", status: "success" } } });
      await Bun.sleep(5);
      const replies = messages().filter((m) => m.t === "host_reply");
      expect(replies.length).toBe(1);
      expect(replies[0].error?.code).toBe("plugin_error");
      expect(replies[0].error?.message).toContain("the warnings cannot be written");
      runtime.onClose();
    }
  });

  test("a run's outcome carries run_done's answer as JSON text: null is an answer, an absent one none", async () => {
    const { runtime, send } = hostRuntime();
    const runs = [0, 1, 2].map(() => runtime.submit("flow", "f/x", "{}"));
    send({ t: "run_done", id: runs[0].id, status: "failure", answer: new Raw('{"outcome":"retry"}') });
    send({ t: "run_done", id: runs[1].id, status: "success", result: new Raw("1"), answer: new Raw("null") });
    send({ t: "run_done", id: runs[2].id, status: "success", result: new Raw("1") });
    const [a, b, c] = await Promise.all(runs.map((r) => r.done));
    expect(a.answer).toBe('{"outcome":"retry"}');
    expect(b.answer).toBe("null");
    expect("answer" in c).toBe(false);
    runtime.onClose();
  });

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

  test("gives a hook its run's data as wl.run.data, frozen, until run_released, in plugin code alone (H11)", async () => {
    const seen: unknown[] = [];
    const { runtime, send, messages } = hostRuntime({
      onModuleStart: () => {
        seen.push(readRunData());
      },
    });
    const call = runDataCall(send);
    call("hc1", "r1", "7", { tenant: "a", list: [1] });
    call("hc2", "r2");
    call("hc3", "r1", "7");
    send({ t: "ping", id: "e1" });
    await Bun.sleep(5);
    send({ t: "run_released", run: "r1", run_data_id: "7" });
    call("hc4", "r1", "7");
    send({ t: "ping", id: "e2" });
    await Bun.sleep(5);
    expect(seen).toEqual([{ tenant: "a", list: [1] }, undefined, { tenant: "a", list: [1] }, undefined]);
    const data = seen[0] as { list: number[] };
    expect(Object.isFrozen(data) && Object.isFrozen(data.list)).toBe(true);
    expect(readRunData()).toBeUndefined();
    expect(messages().filter((m) => m.t === "pong").map((m) => m.runs_held ?? 0)).toEqual([1, 0]);
    expect(runtime.hostHealth().runs_held).toBe(0);
    runtime.onClose();
  });

  test("keeps the data of two runs of one id apart by run_data_id, and empties a run's data for plugin code that runs on past its release", async () => {
    const seen: unknown[] = [];
    let later: unknown = "not read";
    let goOn!: () => void;
    const gate = new Promise<void>((r) => (goOn = r));
    const { runtime, send } = hostRuntime({
      onModuleStart: (e) => {
        seen.push(readRunData());
        // A continuation that the hook leaves behind, which reads the
        // data for the first time once the test lets it go on.
        if (e.site.module === "late") void gate.then(() => (later = readRunData()));
      },
    });
    const call = runDataCall(send);
    call("hc1", "dup", "1", { tag: "a" }, "late");
    call("hc2", "dup", "2", { tag: "b" });
    call("hc3", "dup");
    await Bun.sleep(5);
    send({ t: "run_released", run: "dup", run_data_id: "1" });
    await Bun.sleep(5);
    goOn();
    await gate;
    await Bun.sleep(1);
    call("hc4", "dup", "2");
    call("hc5", "dup", "1");
    await Bun.sleep(5);
    expect(seen).toEqual([{ tag: "a" }, { tag: "b" }, undefined, { tag: "b" }, undefined]);
    expect(later).toBeUndefined();
    expect(runtime.hostHealth().runs_held).toBe(1);
    runtime.onClose();
  });

  test("drops every run's data when the connection ends, also for plugin code that runs on", async () => {
    let later: unknown = "not read";
    let goOn!: () => void;
    const gate = new Promise<void>((r) => (goOn = r));
    const { runtime, send } = hostRuntime({
      onModuleStart: () => {
        void gate.then(() => (later = readRunData()));
      },
    });
    runDataCall(send)("hc1", "r1", "3", { tag: "held" });
    await Bun.sleep(5);
    expect(runtime.hostHealth().runs_held).toBe(1);
    runtime.onClose();
    expect(runtime.hostHealth().runs_held).toBe(0);
    goOn();
    await gate;
    await Bun.sleep(1);
    expect(later).toBeUndefined();
  });

  test("wl.reserve resolves on a reserve_reply without refused, an empty one included, and rejects with its refusal", async () => {
    const outcomes: string[] = [];
    const { runtime, send, sendRaw, messages } = hostRuntime({
      around: [
        {
          selector: { flow: "f/x", module: "a" },
          fn: async (_c, next) => {
            const wl = (globalThis as unknown as { wl: { reserve(n: number): Promise<void> } }).wl;
            outcomes.push(await wl.reserve(10).then(() => "ok", (e: Error) => `${e.name}: ${e.message}`));
            return next();
          },
        },
      ],
    });
    const invoke = (id: string, budget?: boolean) =>
      send({ t: "invoke", id, run: "r", site: "m:a", script: "f/x/a", entry: "main", args: {}, env: {}, boundary: "bun", around: [0], budget });
    const reserveOf = async (inv: string) => {
      for (let i = 0; i < 200; i++) {
        const r = messages().find((m) => m.t === "reserve" && m.inv === inv);
        if (r) return r;
        await Bun.sleep(5);
      }
      throw new Error(`no reserve frame from ${inv}`);
    };
    const settled = async (n: number) => {
      for (let i = 0; i < 200 && outcomes.length < n; i++) await Bun.sleep(5);
    };
    invoke("i1", true);
    const r1 = await reserveOf("i1");
    expect(r1).toEqual({ t: "reserve", id: r1.id, inv: "i1", bytes: 10 });
    // An explicit empty refused, which the canonical encoder would leave
    // out, as a peer may still write it.
    sendRaw(`{"t":"reserve_reply","id":${JSON.stringify(r1.id)},"refused":""}`);
    await settled(1);
    invoke("i2", true);
    send({ t: "reserve_reply", id: (await reserveOf("i2")).id, refused: "no room" });
    await settled(2);
    // Without budget the step reserves nothing, and sends no frame.
    invoke("i3");
    await settled(3);
    expect(outcomes).toEqual(["ok", "RangeError: no room", "ok"]);
    expect(messages().filter((m) => m.t === "reserve").length).toBe(2);
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
