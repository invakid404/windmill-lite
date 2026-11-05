// Developer checks of the test entry (host/testing.ts) against the
// runtime: what an invocation's settlement clears and reports, what the
// process scope keeps, nesting, and the invocation query.

import { expect, test } from "bun:test";
import { AsyncLocalStorage } from "node:async_hooks";
import { PROCESS_SCOPE, type ProcessScope, type StepAPI } from "../runtime/context.ts";
import { currentInvocation, install, invoke } from "../host/testing.ts";

install();

const scope = (globalThis as unknown as Record<symbol, ProcessScope>)[PROCESS_SCOPE];
const wl = (globalThis as unknown as { wl: StepAPI }).wl;

/** Waits ms on a timer that no invocation owns. */
const sleep = (ms: number) => new Promise<void>((resolve) => scope.capture()(() => setTimeout(resolve, ms)));

test("install is idempotent and publishes the process scope and the step API", () => {
  const [timeout, interval, immediate] = [globalThis.setTimeout, globalThis.setInterval, globalThis.setImmediate];
  install();
  expect(globalThis.setTimeout).toBe(timeout);
  expect(globalThis.setInterval).toBe(interval);
  expect(globalThis.setImmediate).toBe(immediate);
  expect(typeof scope.capture).toBe("function");
  expect(typeof wl.signal).toBe("function");
});

test("a step that leaves timers: settlement reports them by kind and clears them", async () => {
  const fired: string[] = [];
  const s = await invoke(() => {
    setTimeout(() => fired.push("timeout"), 5);
    setInterval(() => fired.push("interval"), 5);
    setImmediate(() => fired.push("immediate"));
    return 42;
  });
  expect(s).toEqual({ result: 42, left: { immediate: 1, interval: 1, timeout: 1 } });
  await sleep(30);
  expect(fired).toEqual([]);
});

test("a step that leaves nothing gets an empty report; fired and cleared timers are not counted", async () => {
  const fired: string[] = [];
  const s = await invoke(async () => {
    await new Promise<void>((resolve) => setTimeout(resolve, 1));
    clearTimeout(setTimeout(() => fired.push("cleared"), 1));
    clearInterval(setInterval(() => fired.push("cleared interval"), 1));
    return "done";
  });
  expect(s).toEqual({ result: "done", left: {} });
  await sleep(10);
  expect(fired).toEqual([]);
});

test("work started through the process scope survives settlement and is not reported", async () => {
  // A pooled client captures the scope at load and runs its factory
  // through it, so its maintenance timer belongs to no invocation.
  const inProcess = scope.capture();
  const ticks: (string | undefined)[] = [];
  let monitor: ReturnType<typeof setInterval> | undefined;
  const connect = () => inProcess(() => (monitor ??= setInterval(() => ticks.push(currentInvocation()?.id), 2)));
  const s = await invoke(() => {
    connect();
    return "connected";
  });
  expect(s).toEqual({ result: "connected", left: {} });
  await sleep(20);
  clearInterval(monitor);
  expect(ticks.length).toBeGreaterThan(0);
  expect(new Set(ticks)).toEqual(new Set([undefined]));
});

test("a factory that does not re-enter the process scope starts its work in the step, which settlement clears", async () => {
  let ticks = 0;
  const s = await invoke(() => {
    setInterval(() => ticks++, 2);
  });
  expect(s.left).toEqual({ interval: 1 });
  await sleep(20);
  expect(ticks).toBe(0);
});

test("sequential steps each settle on their own", async () => {
  const first = await invoke(() => {
    setTimeout(() => {}, 50);
    setTimeout(() => {}, 50);
  });
  const second = await invoke(() => {
    setInterval(() => {}, 50);
  });
  expect(first.left).toEqual({ timeout: 2 });
  expect(second.left).toEqual({ interval: 1 });
});

test("a nested step settles on its own, and the outer step goes on in its own invocation", async () => {
  const fired: string[] = [];
  const seen: (string | undefined)[] = [];
  const outer = await invoke(
    async () => {
      setTimeout(() => fired.push("outer"), 5);
      seen.push(currentInvocation()?.id);
      const inner = await invoke(
        () => {
          seen.push(currentInvocation()?.id);
          setTimeout(() => fired.push("inner"), 5);
          return "inner result";
        },
        { id: "inner", run: "r" },
      );
      seen.push(currentInvocation()?.id);
      await sleep(15);
      return inner;
    },
    { id: "outer", run: "r" },
  );
  expect(outer.result).toEqual({ result: "inner result", left: { timeout: 1 } });
  expect(outer.left).toEqual({});
  expect(seen).toEqual(["outer", "inner", "outer"]);
  expect(fired).toEqual(["outer"]);
});

test("concurrent steps keep their own invocations and reports", async () => {
  const [a, b] = await Promise.all([
    invoke(
      async () => {
        await sleep(5);
        setTimeout(() => {}, 50);
        return currentInvocation()?.id;
      },
      { id: "a" },
    ),
    invoke(
      async () => {
        setInterval(() => {}, 50);
        await sleep(10);
        return currentInvocation()?.id;
      },
      { id: "b" },
    ),
  ]);
  expect(a).toEqual({ result: "a", left: { timeout: 1 } });
  expect(b).toEqual({ result: "b", left: { interval: 1 } });
});

test("the invocation query: undefined outside, the step's own inside, settled for a late continuation", async () => {
  expect(currentInvocation()).toBeUndefined();
  let release!: () => void;
  const later = new Promise<void>((resolve) => (release = resolve));
  const late: { inv?: ReturnType<typeof currentInvocation>; fired: boolean } = { fired: false };
  let lateDone!: Promise<void>;
  const s = await invoke(
    () => {
      lateDone = later.then(() => {
        late.inv = currentInvocation();
        setTimeout(() => (late.fired = true), 1);
      });
      return [currentInvocation(), scope.capture()(() => currentInvocation())];
    },
    { id: "step", run: "run" },
  );
  expect(s.result).toEqual([{ id: "step", run: "run", settled: false }, undefined]);
  expect(currentInvocation()).toBeUndefined();
  release();
  await lateDone;
  await sleep(10);
  expect(late).toEqual({ inv: { id: "step", run: "run", settled: true }, fired: false });
});

test("a timer callback runs in its step's invocation; default ids are fresh", async () => {
  const s = await invoke(async () => {
    const inTimer = await new Promise((resolve) => setTimeout(() => resolve(currentInvocation()), 1));
    return [currentInvocation(), inTimer];
  });
  const [inStep, inTimer] = s.result as [ReturnType<typeof currentInvocation>, ReturnType<typeof currentInvocation>];
  expect(inTimer).toEqual(inStep);
  expect(inStep?.id).toMatch(/^[0-9a-f-]{36}$/);
  expect(inStep?.run).toMatch(/^[0-9a-f-]{36}$/);
  const other = await invoke(() => currentInvocation());
  expect(other.result?.id).not.toBe(inStep?.id);
});

test("a step that throws is settled first, then invoke rejects with its error", async () => {
  const fired: string[] = [];
  const err = new Error("boom");
  const p = invoke(async () => {
    setTimeout(() => fired.push("timeout"), 5);
    throw err;
  });
  await expect(p).rejects.toBe(err);
  const sync = invoke(() => {
    setInterval(() => fired.push("interval"), 5);
    throw err;
  });
  await expect(sync).rejects.toBe(err);
  await sleep(20);
  expect(fired).toEqual([]);
});

test("other async-context stores of the caller stay inside the step", async () => {
  const tenant = new AsyncLocalStorage<string>();
  const s = await tenant.run("tenant-1", () => invoke(() => tenant.getStore()));
  expect(s.result).toBe("tenant-1");
});

test("the step API answers inside a step: wl.signal is the invocation's, wl.latch has no run to latch", async () => {
  const s = await invoke(() => {
    const signal = wl.signal();
    let latch = "";
    try {
      wl.latch(new Error("stop"));
    } catch (e) {
      latch = (e as Error).name;
    }
    return [signal.aborted, latch];
  });
  expect(s.result).toEqual([false, "TypeError"]);
  expect(() => wl.signal()).toThrow(TypeError);
});
