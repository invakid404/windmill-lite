// Returns the step's job ids from its environment.
export async function main() {
  const e = process.env;
  return { flow_step: e.WM_FLOW_STEP_ID, job: e.WM_JOB_ID, flow_job: e.WM_FLOW_JOB_ID, root_flow: e.WM_ROOT_FLOW_JOB_ID, root: e.WM_ROOT_JOB_ID };
}
