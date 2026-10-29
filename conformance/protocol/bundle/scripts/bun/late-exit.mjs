// Returns "done" and calls process.exit(9) ms later, after the result, from
// a continuation of a promise-based wait (Bun.sleep), which the
// settle-time fence cannot stop and which keeps the invocation's context.
export function main(ms) {
  Bun.sleep(ms ?? 10).then(() => process.exit(9));
  return "done";
}
