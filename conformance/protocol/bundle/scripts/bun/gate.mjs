// Creates the file started, then waits for the file release and returns
// "released", or, given message, throws an Error with that message. It
// ignores cancellation, so its test sees when the call has started and
// decides when it ends. Relative paths are in the harness's working
// directory.
export async function main(started, release, message) {
  await Bun.write(started, "started");
  while (!(await Bun.file(release).exists())) {
    await Bun.sleep(5);
  }
  if (message !== undefined) throw new Error(message);
  return "released";
}
