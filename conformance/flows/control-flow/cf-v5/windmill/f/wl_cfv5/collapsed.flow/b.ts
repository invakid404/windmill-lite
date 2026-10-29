export async function main(x: number, p: any) {
  return x + (typeof p === "number" ? p : -1000);
}
