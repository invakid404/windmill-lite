// The driver: dispatches f/rp/latching linked to its own job and root, so
// that the dispatched run's jobs belong to this run's tree, and waits for
// it to complete. Its child flow's step latches it, after it writes its
// flow job's id, the child flow's, under this run's flow user state. The
// driver then polls that child flow's job through get_result_maybe and
// get_result, while this run, its tree's root, still runs.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const run = await (
    await fetch(`${base}/jobs/run/f/f/rp/latching?parent_job=${e.WM_JOB_ID}&root_job=${e.WM_ROOT_JOB_ID}`, {
      method: "POST",
      headers: { ...auth, "Content-Type": "application/json" },
      body: "{}",
    })
  ).text();
  for (let i = 0; i < 600; i++) {
    const v = await (await fetch(`${base}/jobs_u/completed/get_result_maybe/${run}`, { headers: auth })).json();
    if (v.completed) break;
    await Bun.sleep(20);
  }
  const flow = await (await fetch(`${base}/jobs/flow/user_states/${e.WM_ROOT_JOB_ID}/latched`, { headers: auth })).json();
  const get = async (path: string) => {
    const res = await fetch(`${base}/${path}`, { headers: auth });
    return { status: res.status, body: await res.text() };
  };
  return {
    maybe: await get(`jobs_u/completed/get_result_maybe/${flow}`),
    result: await get(`jobs_u/completed/get_result/${flow}`),
  };
}
