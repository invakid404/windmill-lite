// Sets the flow user state key key at the run's root, then fails when
// fail is set and the key was not set before: a step that fails on its
// first attempt only.
export async function main(key: string, fail: boolean) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const url = `${base}/jobs/flow/user_states/${root}/${key}`;
  const before = await (await fetch(url, { headers: auth })).json();
  await fetch(url, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "true" });
  if (fail && before === null) throw new Error("first attempt");
  return key;
}
