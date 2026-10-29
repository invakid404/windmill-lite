//nodejs
// WR-50/d: //nodejs takes an ESM codebase script to Node; the
// package.json asset declares the module type.
export function main() {
  return [typeof (globalThis as any).Bun, typeof (process as any).version];
}
