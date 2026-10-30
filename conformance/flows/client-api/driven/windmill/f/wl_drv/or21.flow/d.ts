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
  const t = tag();
  const content = "export function main(x: number) { return { secret: x }; }";
  const schema = { $schema: "https://json-schema.org/draft/2020-12/schema", type: "object", properties: { x: { type: "number" } }, required: ["x"] };
  const mk = async (name: string, extra: Record<string, unknown>) =>
    (await call("POST", "/scripts/create", JSON.stringify({ path: `f/wl_drv/${name}_${t}`, summary: name, description: "", content, language: "bun", schema, ...extra }))).status;
  const created = [await mk("dau", { delete_after_use: true }), await mk("dau_secs", { delete_after_use: true, delete_after_secs: 3600 }), await mk("dau_zero", { delete_after_secs: 0 })];
  const flowCreated = (await call("POST", "/flows/create", JSON.stringify({ path: `f/wl_drv/dau_flow_${t}`, summary: "dau", description: "",
    value: { modules: [{ id: "s", value: { type: "script", path: `f/wl_drv/dau_${t}`, input_transforms: { x: { type: "static", value: 7 } } } }] },
    schema: { $schema: "https://json-schema.org/draft/2020-12/schema", type: "object", properties: {} } }))).status;
  const fid = await start(`/jobs/run/f/f/wl_drv/dau_flow_${t}`, "{}");
  const fr = await done(fid);
  const step = await json("GET", `/jobs_u/completed/get/${(await json("GET", `/jobs_u/get/${fid}`)).flow_status.modules[0].job}`);
  const sync = async (name: string) => {
    const resp = await call("POST", `/jobs/run_wait_result/p/f/wl_drv/${name}_${t}`, '{"x":7}');
    const list = await json("GET", `/jobs/completed/list?script_path_exact=f/wl_drv/${name}_${t}&per_page=10`);
    const j = await json("GET", `/jobs_u/completed/get/${list[0].id}`);
    const logs = (await call("GET", `/jobs_u/get_logs/${list[0].id}`)).body;
    return { response: resp.body, result: j.result, args: j.args, logs: logs.includes("##DELETED##") ? "##DELETED##" : "kept" };
  };
  return { created, flow_created: flowCreated, b: { flow_result: fr.result, step_args: step.args, step_result: step.result }, c: await sync("dau"), d: await sync("dau_secs"), d_zero: await sync("dau_zero") };
}
