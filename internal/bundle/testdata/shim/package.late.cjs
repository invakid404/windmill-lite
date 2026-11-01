// windmill-lite: the registry reads of the late shim of the host-provided module "@acme/ctx" (ADR 0010, D12).
function found() {
  const registry = globalThis[Symbol.for("windmill-lite.hostModules")];
  const m = registry instanceof Map ? registry.get("@acme/ctx") : undefined;
  if (m === undefined || m === null) {
    throw new Error("host-provided module not registered: @acme/ctx (a listed module registered before it imports it lazily, and used it before its registration)");
  }
  return m;
}
Object.defineProperty(exports, "x0", { enumerable: true, get: () => found()["als"] });
Object.defineProperty(exports, "x1", { enumerable: true, get: () => found()["register"] });
Object.defineProperty(exports, "x2", { enumerable: true, get: () => found()["then"] });
