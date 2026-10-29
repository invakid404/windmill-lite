// Waits ms milliseconds, then returns ms.
export async function main(ms: number) {
  await new Promise((r) => setTimeout(r, ms));
  return ms;
}
