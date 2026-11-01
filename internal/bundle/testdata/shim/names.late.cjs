// windmill-lite: the registry reads of the late shim of the host-provided module "f/q\"uote.ts" (ADR 0010, D12).
function found() {
  const registry = globalThis[Symbol.for("windmill-lite.hostModules")];
  const m = registry instanceof Map ? registry.get("f/q\"uote.ts") : undefined;
  if (m === undefined || m === null) {
    throw new Error("host-provided module not registered: f/q\"uote.ts (a listed module registered before it imports it lazily, and used it before its registration)");
  }
  return m;
}
Object.defineProperty(exports, "x0", { enumerable: true, get: () => found()["$ok"] });
Object.defineProperty(exports, "x1", { enumerable: true, get: () => found()["_x1"] });
Object.defineProperty(exports, "x2", { enumerable: true, get: () => found()["a-b"] });
Object.defineProperty(exports, "x3", { enumerable: true, get: () => found()["default"] });
Object.defineProperty(exports, "x4", { enumerable: true, get: () => found()["if"] });
Object.defineProperty(exports, "x5", { enumerable: true, get: () => found()["then"] });
Object.defineProperty(exports, "x6", { enumerable: true, get: () => found()["ünï"] });
