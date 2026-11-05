// The test entry of the Bun host SDK, `windmill-lite/bun/testing` (ADR 0009,
// "The test entry"). A host's process-level code, such as its pooled
// database and cache clients and their timers, runs in the process where
// the steps run, so the settle-time fence decides what of that code a
// step's settlement clears, and the process scope decides what it keeps.
// This entry lets the host's own tests check both without an engine or a
// bundle:
//
// - install() installs the runtime's step machinery in this process;
// - invoke() runs a function as one invocation and settles it with the
//   runtime's own settlement function, settleInvocation;
// - currentInvocation() tells whether code runs inside an invocation, and
//   which one.
//
// It imports only runtime/context.ts, which reads no Bun global when it is
// evaluated or in what this entry calls, so it runs on Bun 1.3.10 and on
// Node 22 or newer. The npm package ships context.ts as one module that
// this entry and the host SDK both import, so a process that loads both has
// one invocation context, one timer fence and one process scope.

import { randomUUID } from "node:crypto";
import {
  installProcessScope,
  installStepAPI,
  installTimers,
  type InvocationContext,
  InvocationLog,
  InvocationResources,
  invocations,
  settleInvocation,
} from "../runtime/context.ts";

/** An invocation, as currentInvocation() reports it. */
export type Invocation = {
  /** The invocation's id: the engine's for a step of a run, or the one
   * that invoke() was given. */
  id: string;
  /** The id of the run that the invocation belongs to. */
  run: string;
  /** Whether the invocation has settled. Code that runs in its context
   * after that, such as a promise continuation that the step queued, is
   * late: the fence clears a timer that it starts at once. */
  settled: boolean;
};

/** The options of invoke(). */
export type InvokeOptions = {
  /** The invocation's id. The default is a random UUID. */
  id?: string;
  /** The id of the invocation's run. The default is a random UUID. */
  run?: string;
};

/** How an invocation that invoke() ran ended. */
export type Settlement<T> = {
  /** What the function returned, or what its promise fulfilled with. */
  result: T;
  /** What the invocation still held when it settled, which settlement
   * released: the count of each kind, in sorted order, such as
   * `{ interval: 1, timeout: 2 }`. It is empty when the invocation left
   * nothing. The runtime reports the same counts as the `left` field of
   * its `left_behind` warning. The kinds are `timeout`, `interval` and
   * `immediate`, and, when a connected host SDK has installed the fetch
   * view and the child-process wrappers in this process, `fetch`, `child`
   * and `detached`. */
  left: Record<string, number>;
};

/**
 * Installs the runtime's step machinery in this process, as the harness
 * installs it before any step loads:
 *
 * - the timer fence: `setTimeout`, `setInterval` and `setImmediate`, on
 *   `globalThis` and on `node:timers`, register each handle that an
 *   invocation starts, so that its settlement clears it;
 * - the process scope, `globalThis[Symbol.for("windmill-lite.processScope")]`,
 *   whose `capture()` returns a runner for work that belongs to no
 *   invocation;
 * - the step API, `globalThis.wl`.
 *
 * A second call, or a call after the host SDK has connected, changes
 * nothing. It installs nothing else: no environment, `fetch` or log view,
 * no child-process wrappers, and no process-level error handlers.
 *
 * Call it in the test's setup, before the test imports a module that
 * imports `node:timers` by name: on Bun 1.3.10 the first named import of a
 * builtin fixes its named bindings for every later importer.
 */
export function install(): void {
  installProcessScope();
  installStepAPI();
  installTimers();
}

/**
 * Runs `fn` as one invocation and settles it as the runtime settles a step.
 *
 * - `fn` runs inside the invocation's async context. Every other
 *   `AsyncLocalStorage` store of the caller stays, as a run's invocations
 *   keep the scope that the host submitted the run in.
 * - Once `fn` has returned, or its promise has settled, the invocation
 *   settles through the runtime's own settlement function: the fence
 *   clears the timers that the invocation still has, and reports them.
 *   Work that `fn` started through the process scope belongs to no
 *   invocation, so settlement neither clears nor reports it.
 * - Inside `fn`, `wl.signal()` returns the invocation's abort signal,
 *   which never aborts here. `wl.latch()` throws a `TypeError`, since no
 *   run waits for the invocation.
 *
 * It calls install() first. Calls may nest: an inner invocation settles
 * on its own, and the caller's invocation goes on.
 *
 * @returns `fn`'s result and what settlement released.
 * @throws What `fn` threw, or what its promise rejected with, once the
 *   invocation has settled.
 */
export async function invoke<T>(fn: () => T | PromiseLike<T>, options: InvokeOptions = {}): Promise<Settlement<Awaited<T>>> {
  install();
  const resources = new InvocationResources();
  const log = new InvocationLog();
  const ctx: InvocationContext = {
    id: options.id ?? randomUUID(),
    run: options.run ?? randomUUID(),
    env: Object.create(null) as Record<string, string>,
    signal: new AbortController().signal,
    resources,
    work: resources,
    log,
  };
  let result: Awaited<T>;
  try {
    // fn's result is awaited inside the invocation's context, as the
    // runtime awaits a step's entrypoint: a thenable that fn returns is
    // resolved there, so the work its then() starts is the invocation's.
    result = await invocations.run(ctx, async () => await fn());
  } catch (e) {
    settleInvocation(ctx);
    throw e;
  }
  return { result, left: settleInvocation(ctx) };
}

/**
 * The invocation whose async context the caller runs in, or `undefined`
 * outside every invocation. It answers for a step that the host SDK's
 * runtime runs as well as for one that invoke() runs. Code in a process
 * scope runs outside every invocation, and so does a timer that work in a
 * process scope started.
 */
export function currentInvocation(): Invocation | undefined {
  const c = invocations.getStore();
  return c === undefined ? undefined : { id: c.id, run: c.run, settled: c.resources.settled };
}
