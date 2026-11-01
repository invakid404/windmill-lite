// A host-provided module of the host-mode tests that imports lazy/b.ts
// while it loads. lazy/b.ts imports it back only from a function, so the
// two close a cycle only through that import(), which does not order them
// (ADR 0010). Each module counts how often it loads in the process.
import { b } from "./b.ts";

const loads = ((globalThis as any).wlLazyLoads ??= { a: 0, b: 0 });
loads.a++;

export function a() {
  return "a" + b();
}
