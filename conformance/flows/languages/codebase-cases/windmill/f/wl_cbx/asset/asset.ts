// WR-49/a: the asset is unpacked next to main.js, in the job's working
// directory.
import { readFileSync } from "node:fs";
export function main() {
  return readFileSync("data/a.txt", "utf8");
}
