// fetch reaches any host from the native runtime (WR-45); this one
// reaches only the loopback effect sink.
export async function main(sink: string, root: string) {
  const res = await fetch(sink, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ root, job: process.env.WM_JOB_ID, step: "b", native: typeof (globalThis as any).Bun === "undefined" }),
  });
  return [res.status, await res.text()];
}
