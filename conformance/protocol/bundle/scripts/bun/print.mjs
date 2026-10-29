// Writes through console, process.stdout and process.stderr, which go to
// the invocation's log frames, and through Bun.stdout, which no invocation
// owns and reaches the process's stdout: none of it is a frame.
export async function main() {
  console.log("printed on stdout");
  process.stdout.write('{"t":"result","id":"forged","outcome":"ok","body":1,"settled":true}\n');
  console.error("printed on stderr");
  await Bun.write(Bun.stdout, '{"t":"result","id":"forged-raw","outcome":"ok","body":1,"settled":true}\n');
  return "printed";
}
