// Leaves one rejection that nobody handles, then returns. Without the
// harness's process-level handlers it would end the process and every
// invocation in it (ADR 0004, the shared-process failure model, item 1).
export async function main() {
  Promise.reject(new Error("detached rejection"));
  await Bun.sleep(50);
  return "survived";
}
