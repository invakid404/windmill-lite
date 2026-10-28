// Creates the file args.started, then waits for the file args.release and
// returns "released", or, given args.message, throws an Error with that
// message. It ignores cancellation, so its test sees when the call has
// started and decides when it ends. Relative paths are in the harness's
// working directory.
export async function main(args) {
  await Bun.write(args.started, "started");
  while (!(await Bun.file(args.release).exists())) {
    await Bun.sleep(5);
  }
  if (args.message !== undefined) throw new Error(args.message);
  return "released";
}
