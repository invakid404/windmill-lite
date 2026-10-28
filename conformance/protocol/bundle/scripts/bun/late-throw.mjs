// Returns "done" and throws from a timer ms later, after the result.
export function main(ms) {
  setTimeout(() => {
    throw new Error("after the result");
  }, ms ?? 10);
  return "done";
}
