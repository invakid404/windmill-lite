// WR-28/e: a static import of ./helper and a dynamic import of a
// different specifier that contains it.
import { h } from "./helper";
export async function main() {
  const m = await import("./nested/./helper");
  return [h(), m.h()];
}
