// Starts a shell that backgrounds `sleep seconds` and exits at once, so the
// sleep is left in the shell's group without it; writes the sleep's pid to
// pidFile and returns it once the shell has exited (WR-55).
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

export async function main(seconds, pidFile) {
  const sh = spawn("sh", ["-c", `sleep ${seconds} >/dev/null 2>&1 & echo $!`], { stdio: ["ignore", "pipe", "ignore"] });
  let out = "";
  sh.stdout.on("data", (d) => (out += d));
  await new Promise((resolve) => sh.on("close", resolve));
  writeFileSync(pidFile, out.trim());
  return Number(out.trim());
}
