// windmill-lite: the registry reads of the late shim of the host-provided module "f/shared/side_effect.ts" (ADR 0010, D12).
function found() {
  const registry = globalThis[Symbol.for("windmill-lite.hostModules")];
  const m = registry instanceof Map ? registry.get("f/shared/side_effect.ts") : undefined;
  if (m === undefined || m === null) {
    throw new Error("host-provided module not registered: f/shared/side_effect.ts (a listed module registered before it imports it lazily, and used it before its registration)");
  }
  return m;
}
Object.defineProperty(exports, "x0", { enumerable: true, get: () => found()["then"] });
