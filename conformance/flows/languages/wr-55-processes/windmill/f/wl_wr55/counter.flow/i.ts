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
export async function main() {
  const r = await fetch(`${base}/api/w/${ws}/jobs/completed/list?script_path_exact=f/wl_wr55/counter&per_page=100`, { headers });
  const list: any[] = await r.json();
  const results = await Promise.all(list.map(async (j) => (await job(j.id)).result));
  return { at_least_two: results.length >= 2, all_n_one: results.every((x) => x.n === 1), pids_distinct: new Set(results.map((x) => x.pid)).size === results.length };
}
