// Imported by main; imports ../wl_wr28c/z, which resolves against
// this script's own path to f/wl_wr28c/z.
import { z } from "../wl_wr28c/z";
export function h() {
  return "lib+" + z();
}
export function main() {
  return h();
}
