// Calls the platform facade named in BASE_URL and returns without waiting
// for the answer. The settle-time fence aborts the call when the invocation
// settles; the outcome of the call is thrown from a process.nextTick
// callback, which keeps the invocation's context, so it reaches the engine
// as a late error (D9).
export function main() {
  fetch(`${process.env.BASE_URL}/api/w/ws1/left`)
    .then(
      () => "answered",
      (e) => e.name,
    )
    .then((r) =>
      process.nextTick(() => {
        throw new Error(`the call ended: ${r}`);
      }),
    );
  return "done";
}
