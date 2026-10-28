// Leaves a rejection nobody handles after args.ms, while the call is
// pending. Without the process-level handlers the walking skeleton (W1)
// installed, it would end the harness process.
export async function main(args) {
  setTimeout(() => Promise.reject(new Error("detached")), args.ms ?? 0);
  return new Promise(() => {});
}
