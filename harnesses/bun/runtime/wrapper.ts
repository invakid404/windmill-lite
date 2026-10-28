// Windmill's Bun wrapper rules for one call of an entrypoint: binding the
// arguments to the signature the build recorded (WR-6, WR-7, WR-12,
// WR-V1), hydrating Date parameters (WR-8, WR-V3), serialising the result
// (WR-13, WR-14) and shaping a caught throw (WR-18). The step runner
// (runtime.ts) applies them in the wrapper's order (WR-2); these functions
// hold the rules themselves and keep no state.

/** One parameter as the build helper records it (bundle.Param): its name,
 * `anonN` for a destructured object, and whether it hydrates to a Date. */
export type Param = { name: string; date: boolean };

/** One export's signature (bundle.Signature): its parameters, or the
 * parse failure that fails a job calling it as its entrypoint (WR-7). */
export type Signature = { params: Param[]; error?: undefined } | { error: string; params?: undefined };

/** The argument that selects another export than main (WR-12). */
export const OVERRIDE = "_ENTRYPOINT_OVERRIDE";

/** The own properties of a thrown value that WR-18 leaves out of extra. */
const NOT_EXTRA = new Set(["line", "name", "stack", "column", "message", "sourceURL", "originalLine", "originalColumn"]);

/** The arguments an entrypoint with these parameters is called with, from
 * the parsed argument object (WR-6):
 *
 * 1. with parameters, each one's value by name, a property read as the
 *    wrapper's destructuring reads it: a missing key gives undefined (so a
 *    default applies), null stays null, extra keys are dropped and nothing
 *    is coerced, except that a Date parameter is hydrated (WR-8);
 * 2. with none, when the arguments carry an override (WR-V1), the values of
 *    every key that does not start with "_", in own-key order: integer-like
 *    keys first, ascending, then the others as the text gave them;
 * 3. with none and no override (an arrow export, a zero-parameter
 *    function), no arguments at all. */
export function bind(params: readonly Param[], args: Record<string, unknown>): unknown[] {
  if (params.length > 0) return params.map((p) => (p.date ? hydrate(args[p.name]) : args[p.name]));
  if (Object.hasOwn(args, OVERRIDE)) return Object.keys(args).filter((k) => !k.startsWith("_")).map((k) => args[k]);
  return [];
}

/** WR-8's run-time test: a truthy value becomes new Date(value), so a
 * number is epoch milliseconds and an unparsable string an Invalid Date;
 * a falsy one ("", 0, null, false, a missing key) becomes undefined. */
export function hydrate(v: unknown): Date | undefined {
  return v ? new Date(v as string | number) : undefined;
}

/** Installs the patch the wrapper installs before it calls the entrypoint
 * (WR-13): a BigInt anywhere in a result or an error serialises as its
 * decimal string. */
export function patchBigInt() {
  (BigInt.prototype as unknown as { toJSON: () => string }).toJSON = function (this: bigint) {
    return this.toString();
  };
}

/** The result's text (WR-13, WR-14): JSON.stringify(value ?? null) with a
 * replacer that maps every undefined to null, so object fields holding
 * undefined are kept as null. A top-level function or symbol has no JSON
 * text; the wrapper's write then fails with a TypeError, whose exact text
 * on Bun 1.3.10 is not recorded (Q-WR-12). */
export function resultText(value: unknown): string {
  const text = JSON.stringify(value ?? null, (_k, v) => (v === undefined ? null : v));
  if (text === undefined) throw new TypeError(`The result has no JSON text: a ${typeof value} cannot be written`);
  return text;
}

/** The error object of a caught throw (WR-18), as its JSON text: message,
 * name and stack read from the thrown value, then step_id when the
 * invocation's WM_FLOW_STEP_ID is not empty, then extra, every other own
 * property, when there is one. It is serialised without the undefined
 * replacer, so fields holding undefined are left out. Reading the fields
 * of null or undefined, a throwing getter, or an extra that cannot be
 * serialised throws: the failure then escapes the wrapper (WR-19). */
export function errorText(e: unknown, stepId: string | undefined): string {
  const x = e as Record<string, unknown>;
  const o: Record<string, unknown> = { message: x.message, name: x.name, stack: x.stack };
  if (stepId) o.step_id = stepId;
  // Own properties of the thrown value, a string's characters included; a
  // null prototype keeps a "__proto__" property a plain key.
  const extra: Record<string, unknown> = Object.create(null);
  let n = 0;
  for (const k of Object.getOwnPropertyNames(x)) {
    if (NOT_EXTRA.has(k)) continue;
    extra[k] = x[k];
    n++;
  }
  if (n > 0) o.extra = extra;
  return JSON.stringify(o);
}

/** The error of a step whose entrypoint's signature did not parse (WR-7,
 * WR-37): Windmill's worker fails the job with it before the job's process
 * starts, so no top-level code runs. */
export function signatureErrorText(message: string, stepId: string | undefined): string {
  const o: Record<string, unknown> = { message: "execution error:\n" + message, name: "ExecutionErr" };
  if (stepId) o.step_id = stepId;
  return JSON.stringify(o);
}

/** The entrypoint's signature in a script's metadata (scripts/bun/<id>.meta.json):
 * what the build recorded for that export, or undefined when it recorded
 * nothing under that name, as for a name the module does not export. */
export function signatureOf(meta: unknown, entry: string): Signature | undefined {
  const sigs = (meta as { signatures?: unknown } | null)?.signatures;
  if (!sigs || typeof sigs !== "object" || !Object.hasOwn(sigs, entry)) return undefined;
  const s = (sigs as Record<string, unknown>)[entry] as Record<string, unknown> | null;
  if (s && typeof s.error === "string") return { error: s.error };
  if (s && Array.isArray(s.params) && s.params.every((p) => p && typeof p.name === "string" && typeof p.date === "boolean")) {
    return { params: s.params as Param[] };
  }
  throw new Error(`the signature of ${JSON.stringify(entry)} in the script's metadata is malformed`);
}
