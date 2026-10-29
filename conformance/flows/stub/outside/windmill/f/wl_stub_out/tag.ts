// Labels a name with the shared library outside the sync root, and with
// the size of the child module the entry ships as an asset from outside
// it: a codebase script of the entry with no format, CommonJS.
import { readFileSync } from "node:fs";
import { label } from "../../../shared/lib/label";

export async function main(name: string) {
  return label(name) + "@" + readFileSync("child.mjs", "utf8").length;
}
