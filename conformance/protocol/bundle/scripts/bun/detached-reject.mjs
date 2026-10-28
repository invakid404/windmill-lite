// Leaves a rejection nobody handles after ms, while the call is pending.
// Without the process-level handlers the walking skeleton (W1) installed,
// it would end the harness process. No invocation's context sees a
// rejection, so it fails nothing (ADR 0004, the shared-process failure
// model, item 3). With size, the error's message is that many "x"s.
export async function main(ms, size) {
  setTimeout(() => Promise.reject(new Error(size ? "x".repeat(size) : "detached")), ms ?? 0);
  return new Promise(() => {});
}
