// Throws the first time it runs for tag in the run's innermost flow
// scope (WM_ROOT_FLOW_JOB_ID), and returns v afterwards: a marker file
// in the worker's /tmp counts the attempts, which the attempts of one
// run share.
import { existsSync, writeFileSync } from "node:fs";

export async function main(tag: string, v: any) {
  const file = `/tmp/wl-ni-${process.env.WM_ROOT_FLOW_JOB_ID}-${tag}`;
  if (!existsSync(file)) {
    writeFileSync(file, "1");
    throw new Error(`first attempt of ${tag}`);
  }
  return v;
}
