export function main(v: number, nonce: string) { if (v === 1) throw new Error("one"); return process.env.WM_JOB_ID; }
