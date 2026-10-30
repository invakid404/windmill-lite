import { spawn } from "node:child_process";
export function main() { spawn("sleep", ["5"], { stdio: "ignore", detached: true }).unref(); return process.env.WM_JOB_ID; }
