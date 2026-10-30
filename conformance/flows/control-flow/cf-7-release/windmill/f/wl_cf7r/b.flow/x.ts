export async function main(v: number, fail: boolean) {
  await new Promise((r) => setTimeout(r, v === 1 ? 4000 : 1000));
  if (fail && v === 2) throw new Error(process.env.WM_JOB_ID);
  return { job: process.env.WM_JOB_ID };
}
