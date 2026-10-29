// Runs f/wl_w2/child through run-and-wait and returns its result;
// the child needs a worker besides the one running this step.
export async function main() {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const url = `${base}/api/w/${process.env.WM_WORKSPACE}/jobs/run_wait_result/p/f/wl_w2/child`;
  const res = await fetch(url, {
    method: "POST",
    headers: { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" },
    body: "{}",
  });
  if (!res.ok) throw new Error(`run_wait_result: ${res.status} ${await res.text()}`);
  return await res.json();
}
