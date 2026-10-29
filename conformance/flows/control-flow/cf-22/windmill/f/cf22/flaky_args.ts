// Throws the first time it runs in the run's innermost flow scope
// (WM_ROOT_FLOW_JOB_ID), and returns its arguments afterwards: a marker
// file in the worker's /tmp counts the attempts.
import { existsSync, writeFileSync } from "node:fs";

export async function main(x: any, y: any) {
  const file = `/tmp/wl-ni-${process.env.WM_ROOT_FLOW_JOB_ID}-args`;
  if (!existsSync(file)) {
    writeFileSync(file, "1");
    throw new Error("first attempt");
  }
  return { x, y };
}
