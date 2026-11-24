// Writes its flow job's id, this child flow's, under the flow user state
// of its effective root, then latches its run (wl.latch).
export async function main() {
  const e = process.env;
  await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs/flow/user_states/${e.WM_ROOT_JOB_ID}/latched`, {
    method: "POST",
    headers: { Authorization: `Bearer ${e.WM_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify(e.WM_FLOW_JOB_ID),
  });
  (globalThis as unknown as { wl: { latch(error: unknown): void } }).wl.latch(new Error("stop"));
  return 1;
}
