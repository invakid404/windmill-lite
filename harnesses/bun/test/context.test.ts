// Developer checks of the per-invocation context (runtime/context.ts). The
// gate is internal/harness's Q-WA-4 test, which runs the stock client on
// the real harness; these pin the view's rules in one process.

import { AsyncLocalStorage } from "node:async_hooks";
import { expect, test } from "bun:test";
import {
  current,
  exitStatus,
  installEnvView,
  installProcessScope,
  installStepAPI,
  type InvocationContext,
  InvocationLog,
  InvocationResources,
  invocations,
  PROCESS_SCOPE,
  type ProcessScope,
  shadow,
  type StepAPI,
} from "../runtime/context.ts";

process.env.WL_CONTEXT_TEST_OWN = "process";
process.env.WL_CONTEXT_TEST_FIRST_WRITE = "process";
installEnvView();
installProcessScope();

function context(env: Record<string, string>): InvocationContext {
  const e = Object.assign(Object.create(null), env);
  shadow(e);
  const resources = new InvocationResources();
  return { id: "i", run: "r", env: e, signal: new AbortController().signal, resources, work: resources, log: new InvocationLog() };
}

function inside<T>(env: Record<string, string>, fn: () => T): T {
  return invocations.run(context(env), fn);
}

test("reads answer from the invocation first, through every name", () => {
  const got = inside({ WL_CONTEXT_TEST_VAR: "inv" }, () => [process.env.WL_CONTEXT_TEST_VAR, Bun.env.WL_CONTEXT_TEST_VAR, import.meta.env.WL_CONTEXT_TEST_VAR]);
  expect(got).toEqual(["inv", "inv", "inv"]);
  expect(process.env.WL_CONTEXT_TEST_VAR).toBeUndefined();
  expect(Bun.env.WL_CONTEXT_TEST_VAR).toBeUndefined();
});

test("a variable the process has is shadowed inside an invocation only", () => {
  expect(inside({ WL_CONTEXT_TEST_OWN: "inv" }, () => [process.env.WL_CONTEXT_TEST_OWN, Bun.env.WL_CONTEXT_TEST_OWN])).toEqual(["inv", "inv"]);
  expect(inside({}, () => Bun.env.WL_CONTEXT_TEST_OWN)).toBe("process");
  expect(Bun.env.WL_CONTEXT_TEST_OWN).toBe("process");
});

test("writes stay with the invocation; copies and enumeration include its variables", () => {
  const got = inside({ WL_CONTEXT_TEST_VAR: "inv" }, () => {
    process.env.WL_CONTEXT_TEST_WRITE = "w";
    return [process.env.WL_CONTEXT_TEST_WRITE, { ...process.env }.WL_CONTEXT_TEST_VAR, Object.keys(process.env).includes("WL_CONTEXT_TEST_VAR"), "WL_CONTEXT_TEST_VAR" in Bun.env];
  });
  expect(got).toEqual(["w", "inv", true, true]);
  expect(process.env.WL_CONTEXT_TEST_WRITE).toBeUndefined();
});

test("a delete stays with the invocation: the process and other invocations keep the variable", () => {
  process.env.WL_CONTEXT_TEST_DEL = "process";
  const got = inside({ WL_CONTEXT_TEST_OWNED: "inv" }, () => {
    delete process.env.WL_CONTEXT_TEST_DEL;
    delete process.env.WL_CONTEXT_TEST_OWNED;
    return [process.env.WL_CONTEXT_TEST_DEL, Bun.env.WL_CONTEXT_TEST_DEL, "WL_CONTEXT_TEST_DEL" in process.env, process.env.WL_CONTEXT_TEST_OWNED, { ...process.env }.WL_CONTEXT_TEST_DEL];
  });
  expect(got).toEqual([undefined, undefined, false, undefined, undefined]);
  expect(process.env.WL_CONTEXT_TEST_DEL).toBe("process");
  expect(inside({}, () => process.env.WL_CONTEXT_TEST_DEL)).toBe("process");
});

test("a variable the process writes while an invocation has its own stays the invocation's", async () => {
  const read = invocations.run(context({ WL_CONTEXT_TEST_LATER: "inv" }), async () => {
    await Bun.sleep(5);
    return [process.env.WL_CONTEXT_TEST_LATER, Bun.env.WL_CONTEXT_TEST_LATER];
  });
  process.env.WL_CONTEXT_TEST_LATER = "process";
  expect(await read).toEqual(["inv", "inv"]);
  expect(Bun.env.WL_CONTEXT_TEST_LATER).toBe("process");
});

test("the first write of a variable the process has shadows it, so Bun.env reads the written value (W1 review, finding 1)", () => {
  const got = inside({}, () => {
    process.env.WL_CONTEXT_TEST_FIRST_WRITE = "inv";
    return [process.env.WL_CONTEXT_TEST_FIRST_WRITE, Bun.env.WL_CONTEXT_TEST_FIRST_WRITE, import.meta.env.WL_CONTEXT_TEST_FIRST_WRITE];
  });
  expect(got).toEqual(["inv", "inv", "inv"]);
  // The process and another invocation keep the process's value.
  expect([process.env.WL_CONTEXT_TEST_FIRST_WRITE, Bun.env.WL_CONTEXT_TEST_FIRST_WRITE]).toEqual(["process", "process"]);
  expect(inside({}, () => Bun.env.WL_CONTEXT_TEST_FIRST_WRITE)).toBe("process");
});

test("the process writes a new variable through either object without reaching the view's prototype again, and Bun.env lists it", () => {
  // Bun 1.4.3's object walks a write of a key it does not hold to its
  // prototype, and once walked back into the trap until the stack overflowed.
  process.env.WL_CONTEXT_TEST_NEW = "a";
  Bun.env.WL_CONTEXT_TEST_NEW_BUN = "b";
  Object.defineProperty(Bun.env, "WL_CONTEXT_TEST_NEW_DEFINED", { value: "c", writable: true, enumerable: true, configurable: true });
  const names = ["WL_CONTEXT_TEST_NEW", "WL_CONTEXT_TEST_NEW_BUN", "WL_CONTEXT_TEST_NEW_DEFINED"];
  expect(names.map((k) => [process.env[k], Bun.env[k], Object.hasOwn(Bun.env, k), Object.keys(Bun.env).includes(k)])).toEqual([
    ["a", "a", true, true],
    ["b", "b", true, true],
    ["c", "c", true, true],
  ]);
  // A value becomes its string, and a symbol key or value is refused as
  // Bun refuses it, through either object.
  (process.env as Record<string, unknown>).WL_CONTEXT_TEST_NEW_OBJECT = { toString: () => "o" };
  expect([process.env.WL_CONTEXT_TEST_NEW_OBJECT, Bun.env.WL_CONTEXT_TEST_NEW_OBJECT]).toEqual(["o", "o"]);
  expect(() => ((process.env as Record<symbol, unknown>)[Symbol("s")] = "x")).toThrow(TypeError);
  expect(() => ((Bun.env as Record<symbol, unknown>)[Symbol("s")] = "x")).toThrow(TypeError);
  expect(() => ((process.env as Record<string, unknown>).WL_CONTEXT_TEST_NEW_SYMBOL = Symbol("v"))).toThrow(TypeError);
  expect(() => ((Bun.env as Record<string, unknown>).WL_CONTEXT_TEST_NEW_SYMBOL = Symbol("v"))).toThrow(TypeError);
  expect(inside({}, () => [
    () => ((process.env as Record<string, unknown>).WL_CONTEXT_TEST_INV_SYMBOL = Symbol("v")),
    () => ((Bun.env as Record<string, unknown>).WL_CONTEXT_TEST_INV_SYMBOL = Symbol("v")),
  ].map((write) => {
    try {
      write();
      return "written";
    } catch (e) {
      return e instanceof TypeError ? "TypeError" : String(e);
    }
  }))).toEqual(["TypeError", "TypeError"]);
});

test("a process write whose value's conversion makes an invocation claim the variable leaves it claimed, through either object", () => {
  for (const [k, write] of [
    ["WL_CONTEXT_TEST_REENTRY", (v: unknown) => ((process.env as Record<string, unknown>).WL_CONTEXT_TEST_REENTRY = v)],
    ["WL_CONTEXT_TEST_REENTRY_BUN", (v: unknown) => ((Bun.env as Record<string, unknown>).WL_CONTEXT_TEST_REENTRY_BUN = v)],
  ] as const) {
    const ctx = context({});
    write({
      toString() {
        invocations.run(ctx, () => {
          process.env[k] = "inv";
        });
        return "host";
      },
    });
    const read = () => [process.env[k], Bun.env[k], import.meta.env[k]];
    expect(invocations.run(ctx, read)).toEqual(["inv", "inv", "inv"]);
    expect([read(), Object.hasOwn(Bun.env, k)]).toEqual([["host", "host", "host"], false]);
    shadow(ctx.env);
    expect(invocations.run(ctx, read)).toEqual(["inv", "inv", "inv"]);
  }
});

test("a variable the process has that an invocation claims answers per invocation through every name, and the process keeps its own value", async () => {
  process.env.WL_CONTEXT_TEST_MOVED = "process";
  const got = inside({ WL_CONTEXT_TEST_MOVED: "inv" }, () => [process.env.WL_CONTEXT_TEST_MOVED, Bun.env.WL_CONTEXT_TEST_MOVED, import.meta.env.WL_CONTEXT_TEST_MOVED, "WL_CONTEXT_TEST_MOVED" in Bun.env]);
  expect(got).toEqual(["inv", "inv", "inv", true]);
  expect([process.env.WL_CONTEXT_TEST_MOVED, Bun.env.WL_CONTEXT_TEST_MOVED, import.meta.env.WL_CONTEXT_TEST_MOVED, "WL_CONTEXT_TEST_MOVED" in Bun.env]).toEqual(["process", "process", "process", true]);
  // process.env lists, copies and describes it, outside and inside.
  expect([Object.keys(process.env).includes("WL_CONTEXT_TEST_MOVED"), { ...process.env }.WL_CONTEXT_TEST_MOVED, Object.getOwnPropertyDescriptor(process.env, "WL_CONTEXT_TEST_MOVED")?.value]).toEqual([true, "process", "process"]);
  expect(inside({ WL_CONTEXT_TEST_MOVED: "inv" }, () => ({ ...process.env }).WL_CONTEXT_TEST_MOVED)).toBe("inv");
  // The process's writes and deletes change its own value only.
  const later = invocations.run(context({ WL_CONTEXT_TEST_MOVED: "inv" }), async () => {
    await Bun.sleep(5);
    return [process.env.WL_CONTEXT_TEST_MOVED, Bun.env.WL_CONTEXT_TEST_MOVED];
  });
  process.env.WL_CONTEXT_TEST_MOVED = "process 2";
  expect([Bun.env.WL_CONTEXT_TEST_MOVED, Object.hasOwn(Bun.env, "WL_CONTEXT_TEST_MOVED")]).toEqual(["process 2", false]);
  Bun.env.WL_CONTEXT_TEST_MOVED = "process 3";
  expect([process.env.WL_CONTEXT_TEST_MOVED, Object.hasOwn(Bun.env, "WL_CONTEXT_TEST_MOVED")]).toEqual(["process 3", false]);
  expect(await later).toEqual(["inv", "inv"]);
  delete process.env.WL_CONTEXT_TEST_MOVED;
  expect([process.env.WL_CONTEXT_TEST_MOVED, Bun.env.WL_CONTEXT_TEST_MOVED, "WL_CONTEXT_TEST_MOVED" in process.env, "WL_CONTEXT_TEST_MOVED" in Bun.env]).toEqual([undefined, undefined, false, false]);
  expect(inside({ WL_CONTEXT_TEST_MOVED: "inv" }, () => Bun.env.WL_CONTEXT_TEST_MOVED)).toBe("inv");
});

test("writes through Bun.env stay with the invocation, except one of a process variable no invocation has claimed", () => {
  process.env.WL_CONTEXT_TEST_UNCLAIMED = "process";
  process.env.WL_CONTEXT_TEST_CLAIMED = "process";
  shadow({ WL_CONTEXT_TEST_CLAIMED: "" });
  const got = inside({}, () => {
    Bun.env.WL_CONTEXT_TEST_BUN_WRITE = "inv";
    Bun.env.WL_CONTEXT_TEST_CLAIMED = "inv";
    Bun.env.WL_CONTEXT_TEST_UNCLAIMED = "inv";
    return [process.env.WL_CONTEXT_TEST_BUN_WRITE, Bun.env.WL_CONTEXT_TEST_BUN_WRITE, process.env.WL_CONTEXT_TEST_CLAIMED, Bun.env.WL_CONTEXT_TEST_CLAIMED];
  });
  expect(got).toEqual(["inv", "inv", "inv", "inv"]);
  expect([Bun.env.WL_CONTEXT_TEST_BUN_WRITE, Bun.env.WL_CONTEXT_TEST_CLAIMED]).toEqual([undefined, "process"]);
  // The residual: Bun's object cannot trap a write of a variable it holds.
  expect(Bun.env.WL_CONTEXT_TEST_UNCLAIMED).toBe("inv");
});

test("a delete through Bun.env is never kept per invocation: it removes an unclaimed process variable and changes nothing for a claimed one", () => {
  process.env.WL_CONTEXT_TEST_DEL_UNCLAIMED = "process";
  process.env.WL_CONTEXT_TEST_DEL_CLAIMED = "process";
  shadow({ WL_CONTEXT_TEST_DEL_CLAIMED: "" });
  const got = inside({}, () => {
    delete Bun.env.WL_CONTEXT_TEST_DEL_UNCLAIMED;
    delete Bun.env.WL_CONTEXT_TEST_DEL_CLAIMED;
    return [process.env.WL_CONTEXT_TEST_DEL_UNCLAIMED, Bun.env.WL_CONTEXT_TEST_DEL_CLAIMED];
  });
  expect(got).toEqual([undefined, "process"]);
  expect([process.env.WL_CONTEXT_TEST_DEL_UNCLAIMED, process.env.WL_CONTEXT_TEST_DEL_CLAIMED]).toEqual([undefined, "process"]);
});

test("enumerating Bun.env lists only the variables it holds: none an invocation has claimed, none of the invocation's own", () => {
  process.env.WL_CONTEXT_TEST_LISTED = "process";
  process.env.WL_CONTEXT_TEST_NOT_LISTED = "process";
  const got = inside({ WL_CONTEXT_TEST_NOT_LISTED: "inv", WL_CONTEXT_TEST_INV_ONLY: "inv" }, () => {
    const keys = Object.keys(Bun.env);
    return [keys.includes("WL_CONTEXT_TEST_LISTED"), keys.includes("WL_CONTEXT_TEST_NOT_LISTED"), keys.includes("WL_CONTEXT_TEST_INV_ONLY"), { ...Bun.env }.WL_CONTEXT_TEST_NOT_LISTED];
  });
  expect(got).toEqual([true, false, false, undefined]);
  expect(Object.keys(Bun.env).includes("WL_CONTEXT_TEST_NOT_LISTED")).toBe(false);
  expect(Object.keys(process.env).includes("WL_CONTEXT_TEST_NOT_LISTED")).toBe(true);
});

test("concurrent invocations each read their own value through every name, across awaits", async () => {
  process.env.WL_CONTEXT_TEST_SHARED = "process";
  const one = (i: number) =>
    invocations.run(context({ WL_CONTEXT_TEST_SHARED: `inv ${i}`, WL_CONTEXT_TEST_ONLY_INV: `only ${i}` }), async () => {
      const seen: (string | undefined)[] = [];
      for (let round = 0; round < 3; round++) {
        await Bun.sleep(1 + ((i * 7 + round) % 4));
        if (i % 2 === 0) process.env.WL_CONTEXT_TEST_WRITTEN = `written ${i}`;
        else Bun.env.WL_CONTEXT_TEST_WRITTEN = `written ${i}`;
        await Promise.resolve();
        seen.push(
          process.env.WL_CONTEXT_TEST_SHARED,
          Bun.env.WL_CONTEXT_TEST_SHARED,
          import.meta.env.WL_CONTEXT_TEST_SHARED,
          Bun.env.WL_CONTEXT_TEST_ONLY_INV,
          Bun.env.WL_CONTEXT_TEST_WRITTEN,
          { ...process.env }.WL_CONTEXT_TEST_WRITTEN,
        );
      }
      return seen;
    });
  const all = await Promise.all(Array.from({ length: 16 }, (_, i) => one(i)));
  all.forEach((seen, i) => expect(seen).toEqual(Array.from({ length: 3 }, () => [`inv ${i}`, `inv ${i}`, `inv ${i}`, `only ${i}`, `written ${i}`, `written ${i}`]).flat()));
  expect([process.env.WL_CONTEXT_TEST_SHARED, Bun.env.WL_CONTEXT_TEST_SHARED, Bun.env.WL_CONTEXT_TEST_ONLY_INV, Bun.env.WL_CONTEXT_TEST_WRITTEN]).toEqual(["process", "process", undefined, undefined]);
});

test("the registry releases what is still pending when the invocation settles, once, and counts it by kind", () => {
  const r = new InvocationResources();
  const released: string[] = [];
  r.track("timeout", () => released.push("t1"));
  const done = r.track("timeout", () => released.push("t2"));
  r.track("fetch", () => {
    released.push("f");
    throw new Error("a failing release");
  });
  r.track("interval", () => released.push("i"));
  done(); // t2 ended by itself
  expect(r.pending()).toEqual({ fetch: 1, interval: 1, timeout: 1 });
  expect(Object.keys(r.pending())).toEqual(["fetch", "interval", "timeout"]);
  expect(r.settle()).toEqual({ timeout: 1, fetch: 1, interval: 1 });
  expect(released).toEqual(["t1", "f", "i"]);
  expect(r.settled).toBe(true);
  expect(r.settle()).toEqual({});
  // Anything tracked after settlement is released at once.
  r.track("timeout", () => released.push("late"));
  expect(released).toEqual(["t1", "f", "i", "late"]);
});

test("a process scope captured inside an invocation leaves the invocation out and keeps every other store", async () => {
  const tenant = new AsyncLocalStorage<string>();
  const scope = (globalThis as Record<symbol, ProcessScope>)[PROCESS_SCOPE];
  const seen = await tenant.run("t1", () =>
    inside({}, () => {
      const inProcess = scope.capture();
      return new Promise<unknown[]>((resolve) => {
        inProcess(() => setTimeout(() => resolve([current()?.id, tenant.getStore()]), 1));
      });
    }),
  );
  expect(seen).toEqual([undefined, "t1"]);
  // Captured outside every invocation, a scope stays outside one run later
  // inside an invocation.
  const outside = scope.capture();
  expect(inside({}, () => outside(() => current()))).toBeUndefined();
  expect(Object.isFrozen(scope)).toBe(true);
});

// Each code as JavaScript source, for exitStatus and for a real Bun.
const EXIT_CODES = [
  "0", "3", "256", "258", "-1", "-256", "4294967301", "9007199254740991", "-9007199254740991",
  "9007199254740992", "-9007199254740992", "1e21", "1.5", "NaN", "Infinity", "-0",
  '"2"', '" 7 "', '" "', '"\\t\\n"', '""', '"x"', '"2.5"', '"0x10"', '"0b11"', '"+5"', '"1e3"', '"-0"',
  '"9007199254740991"', '"9007199254740993"', '"Infinity"',
  "true", "false", "{}", "[]", "[5]", "3n", "new Number(3)",
];

test("process.exit's status, or its refusal, is what a real Bun does with the same code", () => {
  for (const src of EXIT_CODES) {
    const code = eval(`(${src})`);
    let want: unknown;
    try {
      want = exitStatus(code);
    } catch (e) {
      want = [(e as Error).name, (e as Error).message];
    }
    const r = Bun.spawnSync([process.execPath, "-e", `try { process.exit(${src}); } catch (e) { process.stdout.write(JSON.stringify([e.name, e.message])); process.reallyExit(99); }`]);
    const got = r.exitCode === 99 ? JSON.parse(r.stdout.toString()) : r.exitCode;
    expect([src, got]).toEqual([src, want]);
  }
});

test("a code exitStatus refuses leaves process.exitCode as it was", () => {
  const before = process.exitCode;
  expect(exitStatus(undefined)).toBe(typeof before === "number" ? before : 0);
  for (const bad of [1.5, "x", "", true, {}, NaN, 2 ** 53]) {
    expect(() => exitStatus(bad)).toThrow();
  }
  expect(process.exitCode).toBe(before);
});

test("the step API: wl.latch sends its error's text, wl.signal is the invocation's signal (F2c)", () => {
  installStepAPI();
  const wl = (globalThis as { wl?: StepAPI }).wl!;
  // Outside every invocation, as in a host plugin's hook, both refuse.
  expect(() => wl.latch(new Error("x"))).toThrow(TypeError);
  expect(() => wl.signal()).toThrow(TypeError);
  const latched: string[] = [];
  const controller = new AbortController();
  const ctx = { ...context({}), signal: controller.signal, latch: (text: string) => latched.push(text) };
  invocations.run(ctx, () => {
    expect(wl.signal()).toBe(controller.signal);
    wl.latch(new RangeError("too far"));
    wl.latch("a plain text");
    wl.latch({ code: 7 });
    expect(() => wl.latch(undefined)).toThrow(TypeError);
    expect(() => wl.latch(null)).toThrow(TypeError);
  });
  expect(latched).toEqual(["RangeError: too far", "a plain text", '{"code":7}']);
  // A settled invocation's step API refuses, as its facade fetch does.
  ctx.resources.settle();
  invocations.run(ctx, () => {
    expect(() => wl.latch(new Error("late"))).toThrow(TypeError);
    expect(() => wl.signal()).toThrow(TypeError);
  });
  // A step cannot replace it for the steps that share its process.
  expect(() => {
    "use strict";
    (globalThis as Record<string, unknown>).wl = {};
  }).toThrow(TypeError);
  expect((globalThis as { wl?: StepAPI }).wl).toBe(wl);
});
