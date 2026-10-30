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

// Runs the cached flow by path with literal JSON bodies, which keep the
// number's spelling (a tree's YAML would give 1.0 as 1), and compares
// results: a hit returns the first run's stored result, the job id of the
// first run's step. The run's own tag keeps the oracle's repeated runs from
// hitting each other's entries.
async function run(body: string): Promise<{ id: string; result: unknown }> {
  const id = await start("/jobs/run/f/f/wl_fx40r/cached", body);
  return { id, result: (await done(id)).result };
}
const spelling = async (id: string) => (/"n":\s*([^,}\s]+)/.exec((await call("GET", `/jobs_u/get_args/${id}`)).body) ?? [])[1] ?? null;
export async function main() {
  await oracleAPI();
  const t = tag();
  const one = await run(`{"v": "${t}", "n": 1}`);
  const again = await run(`{"v": "${t}", "n": 1}`);
  const pointZero = await run(`{"v": "${t}", "n": 1.0}`);
  return {
    control_hit: again.result === one.result,
    hit_1_0: pointZero.result === one.result,
    stored_n: [await spelling(one.id), await spelling(pointZero.id)],
  };
}
