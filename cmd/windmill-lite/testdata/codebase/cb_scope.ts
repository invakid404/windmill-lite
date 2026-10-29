// The codebase script of milestone C2b's host-mode gate
// (TestHostScopesThroughACodebaseScript): esbuild bundles it as CommonJS,
// the default format of a codebases entry (WR-47), and its import of the
// host-provided module f/w1/scopes.ts resolves to that module's generated
// shim, so it reads the host's scope (ADR 0008 H11, ADR 0010, D12). It
// reads the scope before and after two waits, so that concurrent runs
// interleave, and reads the environment through Bun, which the env rewrite
// points at the invocation's view (ADR 0010 step 5).
import { store } from "./scopes";

export async function main(delay: number) {
  await Bun.sleep(delay);
  const before = store.getStore()?.tenant ?? null;
  await new Promise((resolve) => setTimeout(resolve, delay));
  return {
    tenant: store.getStore()?.tenant ?? null,
    before,
    job: process.env.WM_JOB_ID,
    run: process.env.WM_FLOW_JOB_ID,
    bunEnv: Bun.env.WM_JOB_ID === process.env.WM_JOB_ID,
    commonjs: typeof module === "object" && typeof exports === "object",
  };
}
