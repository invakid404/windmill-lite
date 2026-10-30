import { spawn } from "node:child_process";
export function main() { spawn("sleep", ["30"], { stdio: "ignore" }).unref(); return process.env.WM_JOB_ID; }
