// Throws null, which escapes the wrapper's error shaping (WR-19).
export function main() {
  throw null;
}
