// Calls process.exit(code) after ms, from a timer, while the call is
// pending. The harness intercepts it (D11): the invocation's outcome is
// fixed, and the process lives on.
export async function main(ms, code) {
  setTimeout(() => process.exit(code), ms ?? 0);
  return new Promise(() => {});
}
