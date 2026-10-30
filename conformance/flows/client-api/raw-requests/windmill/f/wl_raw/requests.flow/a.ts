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

export async function main() {
  await oracleAPI();
  const bodies = ['{"s": "x\\u0000y"}', '{"k\\u0000": 1}', '{"x": 1e131072}', '{"k": "\\ud83d"}', '{"k\\ud83d": 1}'];
  // Flow jobs at the path, counted before and after each request (the
  // deployment's own jobs have other kinds).
  const flowJobs = async () => (await json("GET", "/jobs/list?script_path_exact=f/wl_raw/target&job_kinds=flow&per_page=100")).length;
  const refused: Answer[] = [];
  const before = await flowJobs();
  const created: number[] = [];
  for (const b of bodies) {
    refused.push(await call("POST", "/jobs/run/f/f/wl_raw/target", b));
    created.push((await flowJobs()) - before);
  }
  // Q-FX-7: a stored 1.50e3 and its siblings, read back as the job's args.
  const id = await start("/jobs/run/f/f/wl_raw/number", '{"n": 1.50e3, "m": 1.50, "k": 1.50e1}');
  const args = await call("GET", `/jobs_u/get_args/${id}`);
  return { refused, created, stored_args: args.body };
}
