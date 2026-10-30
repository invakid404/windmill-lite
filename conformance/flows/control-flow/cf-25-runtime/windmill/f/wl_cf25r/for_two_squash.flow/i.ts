const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
const ws = process.env.WM_WORKSPACE;
const headers = { Authorization: `Bearer ${process.env.WM_TOKEN}` };
async function job(id: string): Promise<any> {
  const r = await fetch(`${base}/api/w/${ws}/jobs_u/completed/get/${id}`, { headers });
  return await r.json();
}
async function logs(id: string): Promise<string> {
  const r = await fetch(`${base}/api/w/${ws}/jobs_u/get_logs/${id}`, { headers });
  return await r.text();
}
function seconds(j: any): number {
  return (Date.parse(j.completed_at) - Date.parse(j.started_at)) / 1000;
}
// Each iteration's result names its job, and a two-step body's carries the
// first step's result as prev.
export async function main(items: any[]) {
  const all = items.flatMap((r) => (r.prev ? [r.prev, r] : [r]));
  const lines = await Promise.all(all.map(async (r) => (await logs(r.job)).split("\n").some((l) => l.startsWith("dedicated worker bun:"))));
  return {
    values: items.map((r) => r.v),
    runtime_reused: new Set(all.map((r) => r.pid)).size < all.length || all.some((r) => r.n > 1),
    dedicated_worker_log: lines.some((x) => x),
  };
}
