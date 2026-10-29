// Waits ms, then returns a string of size characters: an outcome larger
// than one frame, which goes to the host in parts.
export async function main(ms: number, size: number) {
  await Bun.sleep(ms);
  return "x".repeat(size);
}
