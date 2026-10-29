// A codebase script (SY-8), which esbuild bundles as CommonJS (milestone
// C2b): it reads the host-provided module through its shim, a module of
// its codebase, its entry's define, and the environment through Bun.
import { store } from "../shared/ctx";
import { suffix } from "./lib/suffix";

declare const __FIXTURE_KIND__: string;

export async function main(tag: string) {
  return { tag: tag + suffix(), last: store.get("last") ?? null, kind: __FIXTURE_KIND__, env: Bun.env.WL_FIXTURE ?? null };
}
