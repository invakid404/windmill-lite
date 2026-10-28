// Creates the file started, then waits for the file release and returns
// "released". It ignores cancellation, so its test sees when the call has
// started and decides when it ends.
export async function main(started, release) {
  await Bun.write(started, "started");
  while (!(await Bun.file(release).exists())) {
    await Bun.sleep(5);
  }
  return "released";
}
