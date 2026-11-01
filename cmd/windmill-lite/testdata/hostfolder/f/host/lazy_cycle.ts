// Reads both modules of the lazy cycle (lazy/a.ts and lazy/b.ts) through
// their shims, which take the instances the host registered, and returns
// how often each module loaded in the process.
import { a } from "./lazy/a.ts";
import { later } from "./lazy/b.ts";

export async function main() {
  return [a(), await later(), (globalThis as any).wlLazyLoads];
}
