import { storage } from "./lib/dynamic_builtin.ts";
export async function main() {
  const s = await storage();
  return s.run(1, () => s.getStore());
}
