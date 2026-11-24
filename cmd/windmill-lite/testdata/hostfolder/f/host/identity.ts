// Returns the job identity its environment carries (ADR 0007).
export async function main() {
  const e = process.env;
  return { workspace: e.WM_WORKSPACE, username: e.WM_USERNAME, email: e.WM_EMAIL };
}
