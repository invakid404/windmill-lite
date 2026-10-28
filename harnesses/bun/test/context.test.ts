// Developer checks of the per-invocation context (runtime/context.ts). The
// gate is internal/harness's Q-WA-4 test, which runs the stock client on
// the real harness; these pin the view's rules in one process.

import { expect, test } from "bun:test";
import { installEnvView, invocations, shadow } from "../runtime/context.ts";

process.env.WL_CONTEXT_TEST_OWN = "process";
installEnvView();

function inside<T>(env: Record<string, string>, fn: () => T): T {
  const e = Object.assign(Object.create(null), env);
  shadow(e);
  return invocations.run({ id: "i", run: "r", env: e, signal: new AbortController().signal }, fn);
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
