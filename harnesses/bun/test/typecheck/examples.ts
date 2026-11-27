// Positive and negative examples of the Bun host SDK's public types, which
// check.sh type-checks with the SDK: a RunEnd has warnings, answer or both,
// and nothing else; a host's Plugin keeps a member of its own named
// heldAge, of any type, and a SpawnedPlugin's heldAge is a function. Each
// line under @ts-expect-error must fail to compile, in strict and in
// non-strict mode. check.sh copies this file beside host/ and runtime/,
// where its import resolves.

import type { ConnectOptions, Plugin, RunEnd, SpawnedPlugin, Warning } from "./host/index.ts";

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

// A host's plugin with a member of its own named heldAge, here a number:
// host mode reads no heldAge, so the host's options take it as they always
// have.
const ownMembers = { onRunEnd() {}, heldAge: 5 };
export const hostPlugin: Plugin = ownMembers;
export const hostOptions: Pick<ConnectOptions, "plugin"> = { plugin: ownMembers };

// The plugin of a harness that the engine spawned reports its held age.
export const spawned: SpawnedPlugin = { heldAge: () => 0, onRunEnd: () => [w] };

// @ts-expect-error: a spawned harness's heldAge is a function.
export const spawnedAge: SpawnedPlugin = { heldAge: 5 };
