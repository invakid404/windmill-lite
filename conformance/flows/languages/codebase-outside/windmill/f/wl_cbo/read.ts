// Labels a name with the shared library outside the sync root, and reads
// the note the entry ships as an asset from outside it: a codebase script
// of the entry with no format, CommonJS.
import { readFileSync } from "node:fs";
import { label } from "../../../shared/lib/label";

export function main(name: string) {
  return { label: label(name), note: readFileSync("note.txt", "utf8") };
}
