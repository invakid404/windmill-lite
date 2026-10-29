// 300,000 characters from xorshift32 with the catalogue's seed:
// char = 33 + s % 94 after each step.
export async function main() {
  let s = 2463534242;
  const out: string[] = [];
  for (let i = 0; i < 300000; i++) {
    s ^= s << 13;
    s >>>= 0;
    s ^= s >>> 17;
    s ^= s << 5;
    s >>>= 0;
    out.push(String.fromCharCode(33 + (s % 94)));
  }
  return { data: out.join("") };
}
