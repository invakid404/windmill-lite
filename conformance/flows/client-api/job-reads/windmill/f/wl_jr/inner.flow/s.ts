// Step s of inner: sets the flow user state key k at the run's root, as
// setFlowUserState does, then returns 1.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  await fetch(`${base}/jobs/flow/user_states/${root}/k`, {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: JSON.stringify({ b: 1, a: [1.50, "x"] }),
  });
  return 1;
}
