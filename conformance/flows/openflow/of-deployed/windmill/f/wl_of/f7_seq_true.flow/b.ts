export function main(x: number) {
  if (x === 1) throw new Error("boom");
  return x;
}
