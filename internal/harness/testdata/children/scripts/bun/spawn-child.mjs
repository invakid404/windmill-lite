// Starts `sleep seconds`, detached when asked, with its stdio ignored and
// unreferenced, writes its pid to pidFile when given, and returns the pid
// at once (WR-55/a, WR-55/b).
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

export function main(seconds, detached, pidFile) {
  const child = spawn("sleep", [String(seconds)], { stdio: "ignore", detached });
  child.unref();
  if (pidFile) writeFileSync(pidFile, String(child.pid));
  return child.pid;
}
