import { existsSync, writeFileSync } from "node:fs";

// Throws on its first run in the outer run for index 0, and returns its
// iter_parent otherwise; the marker file is keyed by the outer root.
export function main(i: number, p: any, fail_always: boolean) {
  const marker = `/tmp/wl-cf22h-${process.env.WM_ROOT_JOB_ID}-${i}`;
  const first = !existsSync(marker);
  writeFileSync(marker, "1");
  if (i === 0 && (first || fail_always)) throw new Error("first");
  return p;
}
