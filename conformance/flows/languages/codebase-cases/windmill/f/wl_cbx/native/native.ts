//native
// WR-50/c: //native takes an ESM codebase script into the native runtime.
export function main() {
  return [typeof (globalThis as any).Bun, typeof (process as any).version];
}
