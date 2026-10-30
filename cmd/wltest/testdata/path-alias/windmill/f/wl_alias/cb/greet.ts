// A codebase script whose import resolves only through a tsconfig.json
// path alias.
import { greeting } from "@wl_alias/greeting";

export async function main() {
  return { greeting };
}
