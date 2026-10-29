// A codebase script that reads its asset (F3a's exit gate): Windmill
// unpacks a codebase entry's assets beside its bundle in the job's
// directory, its working directory (WR-49, WR-50); in host mode the host
// SDK extracts them into the host's working directory before any step
// loads (D10). It reads the asset by a relative path and beside its own
// module, and the arguments file the engine writes there (WR-5).
import { readFileSync } from "node:fs";
import { join } from "node:path";

export async function main(tag: string) {
  return {
    relative: readFileSync("data/greeting.txt", "utf8").trim(),
    beside: readFileSync(join(__dirname, "data", "greeting.txt"), "utf8").trim(),
    args: JSON.parse(readFileSync("args.json", "utf8")),
    tag,
  };
}
