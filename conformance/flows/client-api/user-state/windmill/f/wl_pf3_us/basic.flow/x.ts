// An iteration of the parallel loop l (WA-14, WA-V1): it looks its root up
// as the stock client does, sets p<v> there, and reads the k that step a
// set before the loop.
export async function main(v: number) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const set = await fetch(`${base}/jobs/flow/user_states/${root}/p${v}`, {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: JSON.stringify(v),
  });
  await set.text();
  const k = await (await fetch(`${base}/jobs/flow/user_states/${root}/k`, { headers: auth })).json();
  return { v, set: set.status, root_is_env: root === e.WM_ROOT_JOB_ID, own_scope: e.WM_ROOT_FLOW_JOB_ID !== e.WM_ROOT_JOB_ID, k };
}
