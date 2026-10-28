// Reports the process this invocation ran in.
export function main() {
  return { pid: process.pid, cwd: process.cwd() };
}
