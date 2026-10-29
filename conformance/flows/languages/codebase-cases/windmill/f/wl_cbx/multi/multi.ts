// WR-49/b: the file loader makes a second output file, and then the
// entry's assets are not added.
// @ts-ignore: esbuild's file loader
import p from "./blob.bin";
import { existsSync } from "node:fs";
export function main() {
  return [existsSync(p), existsSync("a.txt")];
}
