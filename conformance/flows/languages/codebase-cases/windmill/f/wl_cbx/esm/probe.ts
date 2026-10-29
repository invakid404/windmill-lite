// WR-50/c's control: the same probe on Bun.
export function main() {
  return [typeof (globalThis as any).Bun, typeof (process as any).version];
}
