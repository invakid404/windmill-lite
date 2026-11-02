// A host-provided module that reads the invocation's args.json from the
// working directory, as host code a step calls may: the build cannot read
// the host's instance, so a script that imports it keeps the file in host
// mode (ADR 0004 host mode item 7).
import { readFileSync } from "node:fs";

export function argsP(): unknown {
  return JSON.parse(readFileSync("args.json", "utf8")).p;
}
