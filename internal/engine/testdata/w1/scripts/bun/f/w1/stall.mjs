// Backs up the host's socket to the engine with a large log, then starts
// `sleep 30`, which the runtime gives a process group of its own, writes
// its pid to pidFile, and blocks the event loop for good: the child's
// group must reach the engine though neither the socket nor the event loop
// can carry it (WR-55; milestone FU6, review round 2).
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

export async function main(pidFile) {
  console.log("x".repeat(16 << 20));
  // The log goes to the sender, which writes what the socket takes.
  await new Promise((resolve) => setImmediate(resolve));
  const child = spawn("sleep", ["30"], { stdio: "ignore" });
  writeFileSync(pidFile, String(child.pid));
  for (;;) {}
}
