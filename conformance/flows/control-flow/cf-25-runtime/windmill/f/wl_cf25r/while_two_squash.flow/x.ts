let n = 0;
export function main(v: number, prev: any) { n += 1; console.log(`wl-pid ${process.pid}`); return { n, v, job: process.env.WM_JOB_ID, prev }; }
