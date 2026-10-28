export async function main(x: any) {
  if (x === 2) throw new Error("two");
  return x;
}
