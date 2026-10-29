// Throws the first `fails` times it runs for tag in the outer run
// (WM_ROOT_JOB_ID, which every attempt of a retried subflow step shares),
// and afterwards returns its tag, the attempt it counted, v, and the ids of
// the flow it ran in. A file in the worker's /tmp counts the attempts.
import { existsSync, readFileSync, writeFileSync } from "node:fs";

export async function main(tag: string, fails: number, v: any) {
  const file = `/tmp/wl-n5-${process.env.WM_ROOT_JOB_ID}-${tag}`;
  const n = existsSync(file) ? Number(readFileSync(file, "utf8")) : 0;
  writeFileSync(file, String(n + 1));
  if (n < fails) {
    throw new Error(`attempt ${n} of ${tag}`);
  }
  return { tag, attempt: n, v, flow: process.env.WM_FLOW_JOB_ID, root_flow: process.env.WM_ROOT_FLOW_JOB_ID };
}
