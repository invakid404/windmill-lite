// Positive and negative examples of the Bun host SDK's RunEnd type, which
// check.sh type-checks with the SDK: a RunEnd has warnings, answer or both,
// and nothing else. Each line under @ts-expect-error must fail to compile,
// in strict and in non-strict mode. check.sh copies this file beside host/
// and runtime/, where its import resolves.

import type { Plugin, RunEnd, Warning } from "./host/index.ts";

const w: Warning = { code: "c", message: "m" };

export const accepted: RunEnd[] = [
  { warnings: [w] },
  { answer: { outcome: "retry" } },
  { warnings: [w], answer: 1 },
  { answer: null },
  // An answer of undefined is none, and still a RunEnd.
  { answer: undefined },
  { warnings: undefined },
];

// @ts-expect-error: a RunEnd has warnings, answer or both.
export const empty: RunEnd = {};

// @ts-expect-error: another key is not a RunEnd.
export const other: RunEnd = { answers: 1 };

// @ts-expect-error: warnings are { code, message } objects.
export const bad: RunEnd = { warnings: "w" };

export const plugin: Plugin = {
  onRunEnd: (t) => (t.status === "success" ? { answer: { status: t.status } } : [w]),
};
