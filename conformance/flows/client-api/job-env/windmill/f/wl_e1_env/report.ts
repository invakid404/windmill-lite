// A path script's view of the same variables.
export async function main() {
  const e = process.env;
  return { flow_step: e.WM_FLOW_STEP_ID, flow_path: e.WM_FLOW_PATH, job_path: e.WM_JOB_PATH, job: e.WM_JOB_ID, flow_job: e.WM_FLOW_JOB_ID };
}
