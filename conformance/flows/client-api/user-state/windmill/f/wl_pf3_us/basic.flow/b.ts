// Step b: p1 and none come from the flow_user_state() helper in its input
// transforms (JE-20); it reads p2 through the route.
export async function main(p1: unknown, none: unknown) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const p2 = await (await fetch(`${base}/jobs/flow/user_states/${root}/p2`, { headers: auth })).json();
  return { p1, none, p2 };
}
