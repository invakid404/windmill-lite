// Starts `sleep seconds` and then blocks the event loop for good, so that
// neither a cancel nor a heartbeat is handled: asynchronously, writing the
// child's pid to pidFile and spinning; or, with sync, inside execSync, the
// shell writing its own pid before it becomes the sleep (WR-55).
import { execSync, spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

export function main(seconds, pidFile, sync) {
  if (sync) {
    execSync(`echo $$ > '${pidFile}'; exec sleep ${seconds}`);
    return null;
  }
  const child = spawn("sleep", [String(seconds)], { stdio: "ignore" });
  writeFileSync(pidFile, String(child.pid));
  for (;;) {}
}
