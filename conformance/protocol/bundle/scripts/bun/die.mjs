// Ends the harness process with code after ms, while the call is pending,
// through process.reallyExit, which the harness does not intercept, as a
// crash would: every invocation in the process fails with it.
export async function main(ms, code) {
  setTimeout(() => process.reallyExit(code), ms ?? 0);
  return new Promise(() => {});
}
