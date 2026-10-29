// Writes lines log lines, then calls process.exit(6) in a try whose catch
// and finally blocks each create a file next to marker. In a fresh process
// the process ends at the call, as on Windmill (WR-2), so neither block
// runs; in a warm one the call throws a sentinel, which the catch swallows,
// and both run (D11).
import { writeFileSync } from "node:fs";

export function main(marker, lines) {
  for (let i = 0; i < lines; i++) console.log(`line ${i} ${"x".repeat(90)}`);
  try {
    process.exit(6);
  } catch {
    writeFileSync(`${marker}.catch`, "ran");
  } finally {
    writeFileSync(`${marker}.finally`, "ran");
  }
  return "after the exit";
}
