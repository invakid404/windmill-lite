// Starts a timeout, an interval and an immediate, and returns before any
// of them fires: the settle-time fence reports the three and clears them,
// so none ever runs (D9).
export function main(ms) {
  setTimeout(() => {
    throw new Error("a fenced timeout fired");
  }, ms);
  setInterval(() => {
    throw new Error("a fenced interval fired");
  }, ms);
  setImmediate(() => {
    throw new Error("a fenced immediate fired");
  });
  return "done";
}
