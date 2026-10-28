// Returns "done" and calls process.exit(9) from a timer ms later, after the
// result.
export function main(ms) {
  setTimeout(() => process.exit(9), ms ?? 10);
  return "done";
}
