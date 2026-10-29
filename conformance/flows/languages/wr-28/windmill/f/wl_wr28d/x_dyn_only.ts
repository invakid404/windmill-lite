// WR-28/d: a dynamic import and no static rewrite trigger.
export async function main() {
  const m = await import("./helper");
  return m.h();
}
