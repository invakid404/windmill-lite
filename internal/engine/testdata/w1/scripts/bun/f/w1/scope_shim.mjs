// The step of f/w1/scope as a bundler emits it once every import of the
// host-provided module f/w1/scopes.ts resolves to that module's shim
// (ADR 0010, D12). The shim, the first four statements, is written by hand
// for the walking skeleton; milestone C2a generates it. It takes each
// export from the process-wide registry, where the host SDK put the
// host's own instance before any step loaded.
const registry = globalThis[Symbol.for("windmill-lite.hostModules")];
const scopes = registry?.get("f/w1/scopes.ts");
if (!scopes) throw new Error("host-provided module not registered: f/w1/scopes.ts");
const store = scopes.store;

// Reads the host's scope before and after two waits, so that concurrent
// runs interleave.
export async function main(delay) {
  await Bun.sleep(delay);
  const before = store.getStore()?.tenant ?? null;
  await Bun.sleep(delay);
  return { tenant: store.getStore()?.tenant ?? null, before, job: process.env.WM_JOB_ID, run: process.env.WM_FLOW_JOB_ID };
}
