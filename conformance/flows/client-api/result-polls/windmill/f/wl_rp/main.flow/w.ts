// Step w, the sibling: sets the flow user state key started, then polls
// release every 100 ms until it is true or 60 s have passed, and returns
// 1. The keys are the run's root flow job's (WA-14).
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const state = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs/flow/user_states/${e.WM_ROOT_JOB_ID}`;
  await fetch(`${state}/started`, {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: "true",
  });
  for (let i = 0; i < 600; i++) {
    if ((await (await fetch(`${state}/release`, { headers: auth })).json()) === true) break;
    await Bun.sleep(100);
  }
  return 1;
}
