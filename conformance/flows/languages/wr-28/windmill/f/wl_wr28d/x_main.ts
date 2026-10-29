// WR-28/a: a relative and an absolute static import; lib imports
// ../wl_wr28a/z in turn.
import { h } from "./lib";
import { g } from "/f/wl_wr28b/util";
export async function main() {
  return [h(), g()];
}
