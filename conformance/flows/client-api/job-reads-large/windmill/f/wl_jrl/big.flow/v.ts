// Step v: returns a value of n characters or items, by kind: "x", n times
// "x", which a compressor shrinks to almost nothing; "random", a xorshift
// sequence over 64 letters, which no compressor shrinks; "window", the
// first 8,192 characters of that sequence repeated to n, which only a
// compressor that looks back further than 8 KiB shrinks; "rows", an array
// of n small objects with repeated keys.
export async function main(kind: string, n: number) {
  if (kind === "x") return "x".repeat(n);
  if (kind === "rows") {
    const out: unknown[] = [];
    for (let i = 0; i < n; i++) {
      out.push({ id: i, name: `row-${(i * 7919) % 100003}`, tags: ["a", i % 3 === 0 ? "b" : "c"], score: (i % 97) / 4 });
    }
    return out;
  }
  const a = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  let s = 2463534242;
  const m = kind === "window" ? 8192 : n;
  const out: string[] = [];
  for (let i = 0; i < m; i++) {
    s ^= s << 13;
    s ^= s >>> 17;
    s ^= s << 5;
    out.push(a[(s >>> 0) & 63]);
  }
  const block = out.join("");
  return kind === "window" ? block.repeat(Math.ceil(n / m)).slice(0, n) : block;
}
