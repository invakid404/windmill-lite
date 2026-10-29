// WR-47/b: format esm runs on Bun.
export function main() {
  return typeof (globalThis as any).Bun;
}
