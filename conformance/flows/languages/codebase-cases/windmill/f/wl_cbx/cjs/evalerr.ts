// WR-47/g: requires a module whose evaluation throws, twice. The CLI's
// esbuild 0.28.2 keeps the module's error, so the second require throws it
// again; esbuild 0.28.0's bundle returned the module's empty exports then.
export function main() {
  const seen: string[] = [];
  for (let i = 0; i < 2; i++) {
    try {
      require("./boom.cjs");
      seen.push("ok");
    } catch (e: any) {
      seen.push(e.message);
    }
  }
  return seen;
}
