// The Node side of deviation D13 (wltest check): runs one invocation of a
// CommonJS codebase bundle on Node, where Windmill runs it (WR-30, WR-50),
// so that a step case can compare it with the same bundle on the Bun
// harness.
//
//   node --preserve-symlinks noderun.mjs <entry>
//
// It runs in a job directory laid out as Windmill's Node path lays one out
// (WR-50): the bundle as main.js beside a package.json of {}, the entry's
// assets at their paths, and this driver as the wrapper that imports
// ./main.js. The wrapper rules (binding, Date hydration, the result and
// error serialisation) are the harness's own (harnesses/bun/runtime/
// wrapper.ts, transpiled for Node as ./wl-wrapper.mjs), applied in the
// harness's order, so that the two sides differ only in the runtime and
// its module loader. args.json holds the arguments and meta.json the
// script's metadata. It writes the outcome to outcome.json, as the
// harness's result frame carries it, then exits, which ends any work left
// running (WR-2). A process that exits without writing it ended through
// process.exit or crashed; the runner reads that from its exit status.

import { readFileSync, writeFileSync } from "node:fs";
import { bind, errorText, patchBigInt, resultText, signatureErrorText, signatureOf } from "./wl-wrapper.mjs";

const TAIL_LENGTH = 600;
const entry = process.argv[2];

function describe(e) {
  try {
    if (e instanceof Error) return e.stack ?? `${e.name}: ${e.message}`;
    return String(e);
  } catch {
    return "(an error that cannot be printed)";
  }
}

const escaped = (text, exitCode = 1) => ({ outcome: "escaped", tail: text.slice(-TAIL_LENGTH), exit_code: exitCode });
const stepId = () => process.env.WM_FLOW_STEP_ID || undefined;

function caught(e) {
  try {
    console.error(e);
  } catch {
    // Printing is for the logs; it never changes the outcome.
  }
  try {
    return { outcome: "error", body: errorText(e, stepId()) };
  } catch (inner) {
    return escaped(describe(inner));
  }
}

async function run() {
  let sig;
  try {
    sig = signatureOf(JSON.parse(readFileSync("meta.json", "utf8")), entry);
  } catch (e) {
    return escaped(`windmill-lite: cannot read the metadata of the script: ${describe(e)}`);
  }
  if (sig?.error !== undefined) return { outcome: "error", body: signatureErrorText(sig.error, stepId()) };
  let mod;
  try {
    mod = await import("./main.js");
  } catch (e) {
    return escaped(describe(e));
  }
  const fn = mod[entry];
  if (typeof fn === "function" && sig === undefined) {
    return escaped(`windmill-lite: the bundle records no signature for export ${JSON.stringify(entry)}`);
  }
  let value;
  try {
    if (typeof fn !== "function") throw new Error(`${entry} function is missing`);
    const argv = bind(sig.params, JSON.parse(readFileSync("args.json", "utf8")));
    patchBigInt();
    value = await fn(...argv);
  } catch (e) {
    return caught(e);
  }
  try {
    return { outcome: "ok", body: resultText(value) };
  } catch (e) {
    return caught(e);
  }
}

const outcome = await run();
writeFileSync("outcome.json", JSON.stringify(outcome));
process.exit(0);
