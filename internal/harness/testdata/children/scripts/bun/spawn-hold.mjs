// Starts `sleep seconds` with its stdio ignored, writes its pid to pidFile,
// and never settles, so only a timeout or a cancellation ends the step.
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

export function main(seconds, pidFile) {
  const child = spawn("sleep", [String(seconds)], { stdio: "ignore" });
  writeFileSync(pidFile, String(child.pid));
  return new Promise(() => {});
}
