export async function main(v: number) {
  if (v == 1) throw new Error("one");
  return v;
}
