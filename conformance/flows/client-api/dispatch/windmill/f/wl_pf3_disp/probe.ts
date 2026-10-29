// A dispatched script's view of its own job identity, and the root that a
// lookup of its own job answers, for the step that dispatched it to
// compare with its own.
export async function main() {
  const e = process.env;
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  return {
    job: e.WM_JOB_ID,
    flow_job: e.WM_FLOW_JOB_ID,
    root_flow: e.WM_ROOT_FLOW_JOB_ID,
    root: e.WM_ROOT_JOB_ID,
    lookup: res.ok ? await res.json() : `${res.status}: ${await res.text()}`,
    flow_path: e.WM_FLOW_PATH,
    job_path: e.WM_JOB_PATH,
    step: e.WM_FLOW_STEP_ID,
    state_path: e.WM_STATE_PATH,
  };
}
