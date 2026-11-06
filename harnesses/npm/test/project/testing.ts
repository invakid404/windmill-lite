// The test entry's check in the packaging test (../check.sh). It imports
// windmill-lite/bun/testing from the installed package, as a host's tests
// do, and checks the entry's API against the packed runtime: what a step's
// settlement reports and clears, what the process scope keeps, sequential
// and nested steps, a thenable that a step returns, and the invocation
// query.
//
// check.sh runs it on Bun 1.4.3, and on Node 22 or newer as the
// JavaScript that tsc emits from it (tsconfig.node.json). On Node no Bun
// global exists, so a pass there shows that the entry needs none. It runs
// every check, prints each failure, and exits 1 if any check failed.

import { strict as assert } from "node:assert";
import { AsyncLocalStorage } from "node:async_hooks";
import { currentInvocation, install, type Invocation, invoke, type InvokeOptions, type Settlement } from "windmill-lite/bun/testing";

type ProcessScope = { capture(): <R>(fn: () => R) => R };

const runtime = process.versions.bun === undefined ? `Node ${process.versions.node}` : `Bun ${process.versions.bun}`;
const failures: string[] = [];
let passed = 0;

async function check(name: string, fn: () => Promise<void> | void) {
  try {
    await fn();
    passed++;
  } catch (e) {
    failures.push(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}

install();
const scope = (globalThis as unknown as Record<symbol, ProcessScope | undefined>)[Symbol.for("windmill-lite.processScope")];
if (scope === undefined) {
  console.error(`testing.ts (${runtime}): install() published no process scope`);
  process.exit(1);
}
const inProcess = scope.capture();

/** Waits ms on a timer that no invocation owns. */
const sleep = (ms: number) => new Promise<void>((resolve) => inProcess(() => setTimeout(resolve, ms)));

await check("no Bun global on Node", () => {
  if (process.versions.bun === undefined) assert.equal(typeof (globalThis as { Bun?: unknown }).Bun, "undefined");
});

await check("install() is idempotent and publishes the step API", () => {
  const timers = [globalThis.setTimeout, globalThis.setInterval, globalThis.setImmediate];
  install();
  assert.deepEqual([globalThis.setTimeout, globalThis.setInterval, globalThis.setImmediate], timers);
  assert.equal(typeof (globalThis as { wl?: unknown }).wl, "object");
});

await check("a step's timers are reported by kind and cleared", async () => {
  const fired: string[] = [];
  const s = await invoke(() => {
    setTimeout(() => fired.push("timeout"), 5);
    setInterval(() => fired.push("interval"), 5);
    setImmediate(() => fired.push("immediate"));
    return 42;
  });
  assert.deepEqual(s, { result: 42, left: { immediate: 1, interval: 1, timeout: 1 } });
  await sleep(30);
  assert.deepEqual(fired, []);
});

await check("a step that leaves nothing gets an empty report", async () => {
  const s = await invoke(async () => {
    await new Promise<void>((resolve) => setTimeout(resolve, 1));
    clearTimeout(setTimeout(() => {}, 1));
    return "done";
  });
  assert.deepEqual(s, { result: "done", left: {} });
});

await check("work started through the process scope survives settlement and is not reported", async () => {
  const ticks: (Invocation | undefined)[] = [];
  let monitor: ReturnType<typeof setInterval> | undefined;
  const s = await invoke(() => {
    inProcess(() => (monitor = setInterval(() => ticks.push(currentInvocation()), 2)));
  });
  assert.deepEqual(s.left, {});
  await sleep(20);
  clearInterval(monitor);
  assert.ok(ticks.length > 0, "the interval did not run on after settlement");
  assert.ok(
    ticks.every((t) => t === undefined),
    "the interval ran inside an invocation",
  );
});

await check("sequential and nested steps each settle on their own", async () => {
  const first = await invoke(() => {
    setTimeout(() => {}, 50);
  });
  assert.deepEqual(first.left, { timeout: 1 });
  const fired: string[] = [];
  const seen: (string | undefined)[] = [];
  const outer = await invoke(
    async () => {
      setTimeout(() => fired.push("outer"), 5);
      const inner = await invoke(
        () => {
          seen.push(currentInvocation()?.id);
          setTimeout(() => fired.push("inner"), 5);
        },
        { id: "inner" },
      );
      seen.push(currentInvocation()?.id);
      await sleep(15);
      return inner.left;
    },
    { id: "outer" },
  );
  assert.deepEqual(outer, { result: { timeout: 1 }, left: {} });
  assert.deepEqual(seen, ["inner", "outer"]);
  assert.deepEqual(fired, ["outer"]);
});

await check("the invocation query inside, outside and in a late continuation", async () => {
  assert.equal(currentInvocation(), undefined);
  let release!: () => void;
  const later = new Promise<void>((resolve) => (release = resolve));
  const late: { inv?: Invocation; fired: boolean } = { fired: false };
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
  assert.deepEqual(s.result, [{ id: "step", run: "run", settled: false }, undefined]);
  assert.equal(currentInvocation(), undefined);
  release();
  await lateDone;
  await sleep(10);
  assert.deepEqual(late, { inv: { id: "step", run: "run", settled: true }, fired: false });
});

await check("a thenable that the step returns is resolved inside its invocation, nested too", async () => {
  const seen: (string | undefined)[] = [];
  const fired: string[] = [];
  // A lazy query: its then() starts the work, as a query object of a
  // database client starts its request only when it is awaited.
  const lazyQuery = (tag: string): PromiseLike<string> => ({
    then(onfulfilled, onrejected) {
      seen.push(currentInvocation()?.id);
      setTimeout(() => fired.push(tag), 5);
      return Promise.resolve(tag).then(onfulfilled, onrejected);
    },
  });
  const s = await invoke(() => lazyQuery("lazy"), { id: "lazy" });
  assert.deepEqual(s, { result: "lazy", left: { timeout: 1 } });
  const outer = await invoke(async () => (await invoke(() => lazyQuery("inner"), { id: "inner" })).left, { id: "outer" });
  assert.deepEqual(outer, { result: { timeout: 1 }, left: {} });
  await sleep(20);
  assert.deepEqual(seen, ["lazy", "inner"]);
  assert.deepEqual(fired, []);
});

await check("a step that throws is settled, then invoke rejects with its error", async () => {
  const err = new Error("boom");
  let fired = false;
  await assert.rejects(
    invoke(() => {
      setTimeout(() => (fired = true), 5);
      throw err;
    }),
    (e) => e === err,
  );
  await sleep(20);
  assert.equal(fired, false);
});

await check("the caller's other async-context stores stay inside a step", async () => {
  const tenant = new AsyncLocalStorage<string>();
  const s = await tenant.run("tenant", () => invoke(() => tenant.getStore()));
  assert.equal(s.result, "tenant");
});

await check("the declarations type the API as it behaves", async () => {
  // tsc checks these lines in every setup of check.sh; they also run.
  const options: InvokeOptions = { id: "typed", run: "typed-run" };
  const sync: Settlement<number> = await invoke(() => 1, options);
  const fulfilled: Settlement<string> = await invoke(async () => "text");
  const left: Record<string, number> = sync.left;
  const inv: Invocation | undefined = currentInvocation();
  // @ts-expect-error invoke() takes no option but id and run.
  const extra = () => invoke(() => 1, { id: "typed", signal: undefined });
  // @ts-expect-error a step's result has the type that its function returns.
  const wrong: Settlement<string> = sync;
  assert.deepEqual([sync.result, fulfilled.result, left, inv, typeof extra, wrong.left], [1, "text", {}, undefined, "function", {}]);
});

if (failures.length > 0) {
  for (const f of failures) console.error(`testing.ts (${runtime}): ${f}`);
  process.exit(1);
}
console.log(`testing.ts (${runtime}): windmill-lite/bun/testing passed ${passed} checks`);
