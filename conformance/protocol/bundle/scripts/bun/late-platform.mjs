// Returns "done", then, ms later, from a continuation the settle-time fence
// cannot stop, calls the platform facade named in BASE_URL. The call is
// refused, since the invocation has settled; the refusal is thrown from a
// process.nextTick callback, which keeps the invocation's context, so it
// reaches the engine as a late error (D9).
export function main(ms) {
  Bun.sleep(ms ?? 10)
    .then(() => fetch(`${process.env.BASE_URL}/api/w/ws1/late`))
    .catch((e) =>
      process.nextTick(() => {
        throw e;
      }),
    );
  return "done";
}
