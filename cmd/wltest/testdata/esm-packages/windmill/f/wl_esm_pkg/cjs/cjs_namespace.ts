import * as pkg from "pkg-cjs";
export async function main() {
  return { ping: pkg.ping(), names: Object.keys(pkg) };
}
