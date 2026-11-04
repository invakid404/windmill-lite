// Fails on its first attempt and succeeds on its retry, which it tells
// apart by a flow user state key at the run's root that the first attempt
// sets.
export async function main(key: string) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const url = `${base}/jobs/flow/user_states/${root}/${key}`;
  const tried = await (await fetch(url, { headers: auth })).json();
  if (tried === null) {
    await fetch(url, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "true" });
    throw new Error("first attempt");
  }
  return "second attempt";
}
