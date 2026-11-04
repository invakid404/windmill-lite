import localDefault, { local } from "../lib/local.mjs";
import * as typed from "../lib/typed.mts";
export async function main() {
  return { local, localDefault, kind: typed.kind, twice: typed.twice(21), names: Object.keys(typed) };
}
