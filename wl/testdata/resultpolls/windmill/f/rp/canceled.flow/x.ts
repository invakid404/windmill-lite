// Runs past its 1 s timeout, which cancels its job.
export async function main() {
  await Bun.sleep(5000);
  return 1;
}
