// WR-47/c: esbuild's define replaces __MODE__.
declare const __MODE__: string;
export function main() {
  return __MODE__;
}
