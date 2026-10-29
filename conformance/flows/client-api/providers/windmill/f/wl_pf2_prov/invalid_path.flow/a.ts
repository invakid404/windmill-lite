// Never runs: its argument fails to resolve first (WR-5).
export async function main(x: any) {
  return x;
}
