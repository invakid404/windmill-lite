// Throws from a timer after ms while the call awaits a longer sleep, then
// returns "done". The timer keeps the invocation's context, so the error
// is attributed to it (ADR 0004, the shared-process failure model, item 2).
export async function main(ms) {
  setTimeout(() => {
    throw new Error("from a timer");
  }, ms);
  await Bun.sleep(ms * 3);
  return "done";
}
