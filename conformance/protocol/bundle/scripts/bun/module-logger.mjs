// While it evaluates, starts a timer that writes to stdout ms later. The
// timer is the module's, not the loading invocation's (ADR 0004, loading),
// so once that invocation has settled the line reaches the process's
// stdout instead of being dropped.
setTimeout(() => console.log("module work after settlement"), 30);
export function main() {
  return "loaded";
}
