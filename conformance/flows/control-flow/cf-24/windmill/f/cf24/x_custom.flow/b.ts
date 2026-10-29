// Returns its job path (WM_JOB_PATH, CF-24).
export async function main() {
  return process.env.WM_JOB_PATH;
}
