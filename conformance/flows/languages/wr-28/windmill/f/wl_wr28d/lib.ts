// Imported by x_main; imports ../wl_wr28a/z, which resolves against
// this script's own path to f/wl_wr28a/z.
import { z } from "../wl_wr28a/z";
export function h() {
  return "lib+" + z();
}
export function main() {
  return h();
}
