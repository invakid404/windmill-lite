export async function main(n: number, tag: string) {
  if (n > 100) throw new Error(`too big: ${n}`);
  return `${tag}:${n}`;
}
