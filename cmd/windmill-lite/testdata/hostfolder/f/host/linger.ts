// Waits ms on a timer of its own making, whatever the abort, and returns:
// a step whose timeout abandons it while it still runs, so that its run's
// outcome comes first and its settlement later (ADR 0004 host mode item 5).
export async function main(ms: number) {
  const started = Date.now();
  while (Date.now() - started < ms) await Bun.sleep(10);
  return ms;
}
