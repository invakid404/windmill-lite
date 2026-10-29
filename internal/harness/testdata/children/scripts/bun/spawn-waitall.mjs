// Starts bin, a program that waits for all its children (wait4(-1)) before
// it prints done and exits, and returns what it printed and its exit code
// once it has closed (WR-55; review round 4, finding 2).
import { spawn } from "node:child_process";

export async function main(bin) {
  const child = spawn(bin, ["-test.run=^$"], { env: { ...process.env, WL_WAIT_ALL: "1" }, stdio: ["ignore", "pipe", "ignore"] });
  let out = "";
  child.stdout.on("data", (d) => (out += d));
  const code = await new Promise((resolve) => child.on("close", resolve));
  return { out: out.trim(), code };
}
