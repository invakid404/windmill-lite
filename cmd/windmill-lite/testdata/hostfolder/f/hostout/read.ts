// A codebase script whose entry's relative_path, ../hostlib, lies outside
// the sync root (WR-49): its import of the host-provided module
// ../hostlib/tenant.ts resolves to that module's shim, so it reads the
// scope the host entered (milestone F3h; ADR 0010, D12). It reads before
// and after two waits, so that concurrent runs interleave.
import { tenant } from "../../../hostlib/tenant.ts";

export async function main(delay: number) {
  await Bun.sleep(delay);
  const before = tenant.getStore() ?? null;
  await new Promise((resolve) => setTimeout(resolve, delay));
  return { tenant: tenant.getStore() ?? null, before };
}
