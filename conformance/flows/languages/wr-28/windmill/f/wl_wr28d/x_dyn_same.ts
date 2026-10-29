// WR-28/c: a static import and a dynamic import of the same literal.
import { h } from "./helper";
export async function main() {
  const m = await import("./helper");
  return [h(), m.h()];
}
