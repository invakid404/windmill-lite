// Runs `sh -c script`, its output ignored, and returns its exit code once it
// has closed: a step whose child runs as long as the script does (WR-55).
import { spawn } from "node:child_process";

export async function main(script) {
  const child = spawn("sh", ["-c", script], { stdio: "ignore" });
  return await new Promise((resolve) => child.on("close", resolve));
}
