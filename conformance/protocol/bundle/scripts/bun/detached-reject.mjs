// Leaves a rejection nobody handles after ms, while the call is pending.
// Without the process-level handlers the walking skeleton (W1) installed,
// it would end the harness process. No invocation's context sees a
// rejection, so it fails nothing (ADR 0004, the shared-process failure
// model, item 3). With size, the error's message is that many "x"s. With
// gc, one timer makes the error and the next runs a full collection before
// it rejects: nothing has read the error's stack by then, and Bun 1.3.10
// builds a stack the collection formats without the message.
export async function main(ms, size, gc) {
  const message = size ? "x".repeat(size) : "detached";
  if (!gc) {
    setTimeout(() => Promise.reject(new Error(message)), ms ?? 0);
  } else {
    let e;
    setTimeout(() => {
      e = new Error(message);
    }, ms ?? 0);
    setTimeout(() => {
      Bun.gc(true);
      Promise.reject(e);
    }, (ms ?? 0) + 1);
  }
  return new Promise(() => {});
}
