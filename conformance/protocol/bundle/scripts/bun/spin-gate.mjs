import { existsSync, writeFileSync } from "node:fs";

// Creates the file started, then blocks the event loop until the file
// release exists and returns "released": a CPU-bound step that runs until
// its test ends it.
export function main(started, release) {
  writeFileSync(started, "started");
  while (!existsSync(release)) {}
  return "released";
}
