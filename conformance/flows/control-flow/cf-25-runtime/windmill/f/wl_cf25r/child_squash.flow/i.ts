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
export async function main(ids: string[]) {
  // An id the jobs API does not know reads null.
  return { held: await Promise.all(ids.map(async (id) => { try { return seconds(await job(id)) >= 4.5; } catch { return null; } })) };
}
