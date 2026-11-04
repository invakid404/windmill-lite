import greet, { ping } from "pkg-module";
export async function main() {
  return [ping(), greet()];
}
