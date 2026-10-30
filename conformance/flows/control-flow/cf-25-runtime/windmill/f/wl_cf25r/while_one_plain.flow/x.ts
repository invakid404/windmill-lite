let n = 0;
export function main(v: number, prev: any) { n += 1; return { pid: process.pid, n, v, job: process.env.WM_JOB_ID, prev }; }
