import { helper } from "../../../cjs/helper.ts";
import * as meta from "../lib/meta.cts";
export async function main() {
  return { helper, meta: Object.keys(meta) };
}
