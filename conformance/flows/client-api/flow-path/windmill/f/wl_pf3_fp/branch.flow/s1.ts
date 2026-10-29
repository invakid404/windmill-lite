// A body step: the runnable paths it sees (WA-5, WA-6, FX-46).
export async function main() {
  const e = process.env;
  return { flow_path: e.WM_FLOW_PATH, job_path: e.WM_JOB_PATH, state_path: e.WM_STATE_PATH, step: e.WM_FLOW_STEP_ID };
}
