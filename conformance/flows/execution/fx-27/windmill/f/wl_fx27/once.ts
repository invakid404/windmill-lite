// FX-27/a: the first attempt of a retried step keeps its argument x in a
// file named after the run and throws; the retry reads it back, removes
// the file and returns whether its own x is the same. The attempts of one
// run share the worker's /tmp.
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";

export async function main(x: any) {
  const file = `/tmp/wl-fx27-${process.env.WM_ROOT_FLOW_JOB_ID}`;
  if (!existsSync(file)) {
    writeFileSync(file, JSON.stringify(x));
    throw new Error("first attempt");
  }
  const same = readFileSync(file, "utf8") === JSON.stringify(x);
  rmSync(file);
  return same;
}
