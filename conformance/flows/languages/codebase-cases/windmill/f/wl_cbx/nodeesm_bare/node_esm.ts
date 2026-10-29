//nodejs
// WR-50/e: //nodejs without a declared module type.
export function main() {
  return [typeof (globalThis as any).Bun, process.version];
}
