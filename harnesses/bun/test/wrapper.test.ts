// Developer checks of Windmill's Bun wrapper rules (runtime/wrapper.ts).
// The gate is the step suite (conformance/steps, wltest check), which runs
// them through the real harness; these pin the rules in one process.

import { expect, test } from "bun:test";
import { bind, errorText, hydrate, patchBigInt, resultText, signatureErrorText, signatureOf, stepContext } from "../runtime/wrapper.ts";

const p = (name: string, date = false) => ({ name, date });

test("named binding: missing keys are undefined, null stays, extras are dropped, nothing is coerced (WR-6)", () => {
  expect(bind([p("a"), p("b"), p("c")], { a: "5", b: null, zzz: 9 })).toEqual(["5", null, undefined]);
});

test("Date parameters hydrate truthy values only (WR-8)", () => {
  const [d, e, n] = bind([p("d", true), p("e", true), p("n", true)], { d: "2024-01-02T03:04:05Z", e: "", n: 0 }) as [Date, unknown, unknown];
  expect(d.toISOString()).toBe("2024-01-02T03:04:05.000Z");
  expect([e, n]).toEqual([undefined, undefined]);
  expect((hydrate(1700000000000) as Date).toISOString()).toBe("2023-11-14T22:13:20.000Z");
  expect(Number.isNaN((hydrate("nope") as Date).getTime())).toBe(true);
});

test("with no parameters, an override passes the non-underscore values and no override passes none (WR-V1)", () => {
  expect(bind([], JSON.parse('{"_ENTRYPOINT_OVERRIDE":"o","b":1,"9":9,"2":2,"_x":7,"a":3}'))).toEqual([2, 9, 1, 3]);
  expect(bind([], { a: 1 })).toEqual([]);
});

test("the result keeps undefined fields as null and serialises a BigInt as its string (WR-13, WR-14)", () => {
  patchBigInt();
  expect(resultText({ a: undefined, f() {}, n: 10n, x: [undefined, NaN, -0] })).toBe('{"a":null,"n":"10","x":[null,null,0]}');
  expect(resultText(undefined)).toBe("null");
  expect(() => resultText(() => 1)).toThrow(TypeError);
});

test("the step context keys come from the job's environment, each only when it is not empty (FX-12)", () => {
  const env = { WM_FLOW_PATH: "f/x/loop-1", WM_JOB_ID: "j", WM_FLOW_STEP_ID: "a", WM_ROOT_JOB_ID: "r" };
  expect(Object.entries(stepContext(env))).toEqual([
    ["step_id", "a"],
    ["job_id", "j"],
    ["flow_path", "f/x/loop-1"],
  ]);
  // A script run on its own has no step id and no parent path.
  expect(stepContext({ WM_JOB_ID: "j", WM_FLOW_STEP_ID: "", WM_FLOW_PATH: "" })).toEqual({ job_id: "j" });
  expect(stepContext({})).toEqual({});
});

test("the error object: message, name, stack, step_id, job_id, flow_path, then extra (WR-18, FX-12)", () => {
  patchBigInt();
  const ctx = { step_id: "a", job_id: "j", flow_path: "f/x" };
  const e = Object.assign(new Error("x"), { code: 5, big: 2n });
  const o = JSON.parse(errorText(e, ctx));
  expect(Object.keys(o)).toEqual(["message", "name", "stack", "step_id", "job_id", "flow_path", "extra"]);
  expect([o.message, o.name, o.step_id, o.job_id, o.flow_path, o.extra]).toEqual(["x", "Error", "a", "j", "f/x", { code: 5, big: "2" }]);
  expect(errorText("ab", {})).toBe('{"extra":{"0":"a","1":"b","length":2}}');
  // FX-47/a: a thrown string gets the keys before its characters.
  expect(errorText("ab", ctx)).toBe('{"step_id":"a","job_id":"j","flow_path":"f/x","extra":{"0":"a","1":"b","length":2}}');
  expect(errorText(7, ctx)).toBe('{"step_id":"a","job_id":"j","flow_path":"f/x"}');
  expect(errorText(7, { job_id: "j" })).toBe('{"job_id":"j"}');
  expect(errorText({ message: undefined, __proto__x: 1 }, {})).toBe('{"extra":{"__proto__x":1}}');
  // An own property of the same name is extra, not a context key.
  expect(errorText({ message: "m", job_id: "mine" }, ctx)).toBe('{"message":"m","step_id":"a","job_id":"j","flow_path":"f/x","extra":{"job_id":"mine"}}');
  expect(() => errorText(null, ctx)).toThrow(TypeError);
  const cyclic: Record<string, unknown> = { message: "c" };
  cyclic.self = cyclic;
  expect(() => errorText(cyclic, ctx)).toThrow(TypeError);
});

test("a signature that did not parse is the worker's execution error, with step_id alone (WR-7, WR-37, FX-12)", () => {
  expect(signatureErrorText("parameter syntax unsupported: x", "a")).toBe('{"message":"execution error:\\nparameter syntax unsupported: x","name":"ExecutionErr","step_id":"a"}');
});

test("signatures are read by export name from the metadata, own names only", () => {
  const meta = { signatures: { main: { params: [p("a", true)] }, bad: { error: "parameter syntax unsupported: r" } } };
  expect(signatureOf(meta, "main")).toEqual({ params: [p("a", true)] });
  expect(signatureOf(meta, "bad")).toEqual({ error: "parameter syntax unsupported: r" });
  expect(signatureOf(meta, "constructor")).toBeUndefined();
  expect(signatureOf({}, "main")).toBeUndefined();
  expect(() => signatureOf({ signatures: { main: { params: [{ name: 1 }] } } }, "main")).toThrow();
});
