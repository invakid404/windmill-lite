// Returns "done" and throws ms later, after the result, from work the
// settle-time fence cannot stop: a continuation of a promise-based wait
// (Bun.sleep), which keeps the invocation's context. It throws from a
// process.nextTick callback, whose throw keeps that context too, so the
// error is attributed to the settled invocation (D9).
export function main(ms) {
  Bun.sleep(ms ?? 10).then(() =>
    process.nextTick(() => {
      throw new Error("after the result");
    }),
  );
  return "done";
}
