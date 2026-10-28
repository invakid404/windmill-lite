// Creates the file args.started, then waits for the file args.release and
// returns "released". It ignores cancellation, so its test sees when the
// call has started and decides when it ends.
export async function main(args) {
  await Bun.write(args.started, "started");
  while (!(await Bun.file(args.release).exists())) {
    await Bun.sleep(5);
  }
  return "released";
}
