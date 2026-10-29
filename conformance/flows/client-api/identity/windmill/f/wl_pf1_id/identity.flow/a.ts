// The job identity an inline flow step sees (WA-4, WA-5, WR-22, WA-2), and
// the root that a platform call made with its own token answers (the root
// lookup of WA-14, WA-V1).
export async function main() {
  const e = process.env;
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  return {
    state_path: e.WM_STATE_PATH,
    base: [e.BASE_INTERNAL_URL ?? null, typeof e.BASE_URL],
    token: typeof e.WM_TOKEN,
    root_status: res.status,
    root: await res.json(),
    root_env: e.WM_ROOT_JOB_ID,
  };
}
