import { existsSync, writeFileSync } from "node:fs";

// Creates the file args.started, then blocks the event loop until the file
// args.release exists and returns "released": a CPU-bound step that runs
// until its test ends it.
export function main(args) {
  writeFileSync(args.started, "started");
  while (!existsSync(args.release)) {}
  return "released";
}
