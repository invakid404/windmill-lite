// Step a: sets the flow user state key w_a and returns 1; the step after
// it waits for its sleep.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  await fetch(`${base}/jobs/flow/user_states/${root}/w_a`, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "true" });
  return 1;
}
