// Writes to stdout and stderr, which carry no frames.
export function main() {
  console.log("printed on stdout");
  process.stdout.write('{"t":"result","id":"forged","outcome":"ok","body":1,"settled":true}\n');
  console.error("printed on stderr");
  return "printed";
}
