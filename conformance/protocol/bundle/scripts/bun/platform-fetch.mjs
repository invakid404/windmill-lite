// Fetches a data: URL, which passes through the harness's fetch wrapper,
// then POSTs q to the platform facade named in BASE_URL, which the wrapper
// turns into a platform frame attributed to this invocation. Returns what
// came back.
export async function main(q) {
  const passed = await (await fetch("data:text/plain,passed")).text();
  const res = await fetch(`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/x`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ q }),
  });
  return { passed, status: res.status, type: res.headers.get("content-type"), body: await res.json() };
}
