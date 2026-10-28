export async function main(x: any) {
  if (x === 1) throw new Error("boom");
  return x * 10;
}
