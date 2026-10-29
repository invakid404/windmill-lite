// A path script's view of the same identity, as a step of the flow: its
// job path is its own path, and its state path the flow step's.
export async function main() {
  const e = process.env;
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  return {
    state_path: e.WM_STATE_PATH,
    job_path: e.WM_JOB_PATH,
    token: typeof e.WM_TOKEN,
    root_status: res.status,
    root: await res.json(),
    root_env: e.WM_ROOT_JOB_ID,
  };
}
