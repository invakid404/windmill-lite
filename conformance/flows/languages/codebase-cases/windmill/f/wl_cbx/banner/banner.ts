// WR-47/f: esbuild's banner runs before the bundle.
export function main() {
  return (globalThis as any).__banner;
}
