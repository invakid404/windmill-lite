// How a value comes back from flow user state: it is written as raw JSON
// text, with keys out of jsonb's order and numbers spelled as a client
// would not write them, and read back as text, which shows the key order
// and the number spelling the state keeps.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const set = await fetch(`${base}/jobs/flow/user_states/${root}/v`, {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: '{"b":1,"a":1.0,"ab":1e3,"c":{"y":[1.50,"\\u0041"],"x":null}}',
  });
  await set.text();
  const text = await (await fetch(`${base}/jobs/flow/user_states/${root}/v`, { headers: auth })).text();
  return { set: set.status, text, keys: Object.keys(JSON.parse(text)) };
}
