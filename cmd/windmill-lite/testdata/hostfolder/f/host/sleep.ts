// Resolves with ms after waiting that long on a timer.
export async function main(ms: number) {
  await Bun.sleep(ms);
  return ms;
}
