//native
// Dispatches a script on the Windmill at base as the stock client does,
// with the native realm's fetch.
export async function main(base: string) {
  const job = process.env.WM_JOB_ID;
  const root = process.env.WM_ROOT_FLOW_JOB_ID;
  const r = await fetch(`${base}/api/w/${process.env.WM_WORKSPACE}/jobs/run/p/f/remote/script?parent_job=${job}&root_job=${root}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ x: 1 }),
  });
  return { job, root, out: await r.text() };
}
