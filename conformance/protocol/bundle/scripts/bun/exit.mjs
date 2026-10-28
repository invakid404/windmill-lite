// Ends the harness process with args.code after args.ms, while the call is
// pending. Milestone C3a intercepts process.exit; until then it ends the
// process.
export async function main(args) {
  setTimeout(() => process.exit(args.code), args.ms ?? 0);
  return new Promise(() => {});
}
