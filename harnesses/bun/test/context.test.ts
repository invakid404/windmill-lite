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
  type InvocationContext,
  InvocationLog,
  InvocationResources,
  invocations,
  PROCESS_SCOPE,
  type ProcessScope,
  shadow,
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
