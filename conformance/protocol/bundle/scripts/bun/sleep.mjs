// Resolves with ms after waiting that long on a timer.
export async function main(ms) {
  await Bun.sleep(ms);
  return ms;
}
