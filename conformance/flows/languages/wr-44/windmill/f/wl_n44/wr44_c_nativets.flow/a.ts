export async function main() {
  const keep: number[][] = [];
  for (let i = 0; i < 64; i++) {
    keep.push(new Array(1_000_000).fill(i + 0.5));
    await new Promise(r => setTimeout(r, 0));
  }
  return keep.length;
}
