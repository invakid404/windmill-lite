export async function main(n: number) {
  if (n > 100) throw new Error("too big");
  return `n=${n}`;
}
