// Leaves a rejection nobody handles after args.ms, while the call is
// pending. Without the process-level handlers of milestone C3a it ends the
// harness process.
export async function main(args) {
  setTimeout(() => Promise.reject(new Error("detached")), args.ms ?? 0);
  return new Promise(() => {});
}
