// Computes synchronously for ms milliseconds, blocking the event loop, and
// returns (case watchdog/finite-cpu of ADR 0004).
export function main(ms: number) {
  const end = Date.now() + ms;
  let turns = 0;
  while (Date.now() < end) turns++;
  return { ms, computed: turns > 0 };
}
