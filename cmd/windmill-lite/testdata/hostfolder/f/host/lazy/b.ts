// The other module of lazy/a.ts's cycle: it imports lazy/a.ts only when
// later() is called.
const loads = ((globalThis as any).wlLazyLoads ??= { a: 0, b: 0 });
loads.b++;

export function b() {
  return "b";
}

export async function later() {
  const { a } = await import("./a.ts");
  return a();
}
