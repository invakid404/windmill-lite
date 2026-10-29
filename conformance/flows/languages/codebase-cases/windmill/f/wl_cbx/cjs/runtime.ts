// WR-47/a: an entry with no format is CommonJS, which runs on Node.
export function main() {
  return typeof (globalThis as any).Bun;
}
