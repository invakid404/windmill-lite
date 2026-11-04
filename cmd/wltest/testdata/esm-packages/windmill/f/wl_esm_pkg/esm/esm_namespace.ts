import * as pkg from "pkg-esm";
export async function main() {
  return { ping: pkg.ping(), names: Object.keys(pkg) };
}
