import { spawn } from "node:child_process";
export function main(v: number) { spawn("sleep", ["5"], { stdio: "ignore" }).unref(); return process.env.WM_JOB_ID; }
