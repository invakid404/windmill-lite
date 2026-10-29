// Reads the host's scope through the host-provided module, whose import
// the build resolves to the module's shim (ADR 0010, D12), before and
// after two waits, so that concurrent runs interleave.
import { store } from "./scopes";

export async function main(delay: number) {
  await Bun.sleep(delay);
  const before = store.getStore()?.tenant ?? null;
  await Bun.sleep(delay);
  return { tenant: store.getStore()?.tenant ?? null, before, job: process.env.WM_JOB_ID, run: process.env.WM_FLOW_JOB_ID };
}
