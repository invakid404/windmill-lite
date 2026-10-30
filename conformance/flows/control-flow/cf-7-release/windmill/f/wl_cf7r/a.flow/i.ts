// Talks to the oracle's API with the step's own token (a black-box client,
// as the harness is), so a recording can hold what only the API shows.
const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
const ws = process.env.WM_WORKSPACE!;
const root = process.env.WM_ROOT_FLOW_JOB_ID!;
const auth = { Authorization: `Bearer ${process.env.WM_TOKEN}` };
type Answer = { status: number; type: string | null; body: string };
async function call(method: string, path: string, body?: string, type: string | null = "application/json"): Promise<Answer> {
  const headers: Record<string, string> = { ...auth };
  if (body !== undefined && type !== null) headers["Content-Type"] = type;
  const r = await fetch(`${base}/api/w/${ws}${path}`, { method, headers, body });
  return { status: r.status, type: r.headers.get("content-type"), body: await r.text() };
}
async function json(method: string, path: string, value?: unknown): Promise<any> {
  const a = await call(method, path, value === undefined ? undefined : JSON.stringify(value));
  if (a.status >= 300) throw new Error(`${method} ${path}: ${a.status} ${a.body}`);
  return a.body === "" ? null : JSON.parse(a.body);
}
// Starts a job and returns its id, failing at once on a refused request.
async function start(path: string, body = "{}"): Promise<string> {
  const a = await call("POST", path, body);
  if (a.status >= 300) throw new Error(`POST ${path}: ${a.status} ${a.body}`);
  return a.body.replaceAll('"', "");
}
async function done(id: string, secs = 100): Promise<any> {
  for (let i = 0; i < secs * 4; i++) {
    const r = await json("GET", `/jobs_u/completed/get_result_maybe/${id}`);
    if (r.completed) return r;
    await new Promise((res) => setTimeout(res, 250));
  }
  throw new Error(`job ${id} did not complete`);
}
const sleep = (ms: number) => new Promise((res) => setTimeout(res, ms));
// A driver reads the oracle's API: anywhere else (windmill-lite's facade
// answers only the routes a step's client makes) it fails at once.
async function oracleAPI() {
  const r = await fetch(`${base}/api/version`);
  const v = r.ok ? await r.text() : "";
  if (!v.startsWith("CE v")) throw new Error(`the driver needs Windmill's API; /api/version answered ${r.status} ${v}`);
}
const tag = () => root.replaceAll("-", "").slice(0, 12);

export async function main(items: any[]) {
  const ids = items.map((r) => r.job ?? r.error.message);
  const jobs = await Promise.all(ids.map((id: string) => json("GET", `/jobs_u/completed/get/${id}`)));
  const start = jobs.map((j) => Date.parse(j.started_at));
  const end = jobs.map((j) => (j.completed_at ? Date.parse(j.completed_at) : Date.parse(j.started_at) + j.duration_ms));
  const ends01 = [end[0], end[1]].sort((a, b) => a - b);
  const ends012 = [end[0], end[1], end[2]].sort((a, b) => a - b);
  return {
    first_two_start_before_any_completion: Math.max(start[0], start[1]) < Math.min(end[0], end[1]),
    third_released_by_first_completion: start[2] >= ends01[0] && start[2] - ends01[0] < 6000,
    fourth_released_by_second_completion: start[3] >= ends012[1] && start[3] - ends012[1] < 6000,
  };
}
