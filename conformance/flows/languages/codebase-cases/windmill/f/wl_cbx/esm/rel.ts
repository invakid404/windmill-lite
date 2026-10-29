// A relative import between codebase files, bundled by esbuild, and the
// step's environment through Bun.env, in the esm entry.
import { tag } from "./lib/tag";
export function main(x: number) {
  return { tag: tag("esm"), x, step: Bun.env.WM_FLOW_STEP_ID };
}
