// Blocks the event loop for ms, then returns ms.
export function main(ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {}
  return ms;
}
