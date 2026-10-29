// A relative import between codebase files, bundled by esbuild, and the
// step's environment, in the CommonJS entry.
import { tag } from "./lib/tag";
export function main(x: number) {
  return { tag: tag("cjs"), x, step: process.env.WM_FLOW_STEP_ID };
}
