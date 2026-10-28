// WA-4/a's step b, which also returns its own job id and what step a
// returned.
export async function main(prev: unknown) {
  const e = process.env;
  return {
    flow_step: e.WM_FLOW_STEP_ID,
    flow_path: e.WM_FLOW_PATH,
    job_path: e.WM_JOB_PATH,
    flow_job_is_parent: e.WM_FLOW_JOB_ID !== "",
    job: e.WM_JOB_ID,
    prev,
  };
}
