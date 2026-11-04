// Step f: on its first attempt sets the flow user state key w_f and fails;
// its retry, a few seconds later, returns "second attempt".
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const url = `${base}/jobs/flow/user_states/${root}/w_f`;
  if ((await (await fetch(url, { headers: auth })).json()) === null) {
    await fetch(url, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "true" });
    throw new Error("first attempt");
  }
  return "second attempt";
}
