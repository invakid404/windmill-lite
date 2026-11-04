// Step v: returns a string of n characters, either n times "x", which a
// compressor shrinks to almost nothing, or a xorshift sequence over 64
// letters, which no compressor shrinks.
export async function main(kind: string, n: number) {
  if (kind === "x") return "x".repeat(n);
  const a = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  let s = 2463534242;
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    s ^= s << 13;
    s ^= s >>> 17;
    s ^= s << 5;
    out.push(a[(s >>> 0) & 63]);
  }
  return out.join("");
}
