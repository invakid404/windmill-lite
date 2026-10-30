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

async function preview(content: string, args: Record<string, unknown> = {}) {
  const id = await start("/jobs/run/preview", JSON.stringify({ content, language: "bun", args }));
  const r = await done(id);
  return { success: r.success, result: r.result };
}
export async function main() {
  await oracleAPI();
  const flow = { modules: [
    { id: "a", value: { type: "rawscript", language: "bun", content: "export function main() { return 9; }", input_transforms: {} } },
    { id: "l", stop_after_if: { expr: "true", skip_if_stopped: false }, value: { type: "whileloopflow", skip_failures: false, modules: [
      { id: "b", value: { type: "rawscript", language: "bun", content: "export function main(p: any) { return p; }", input_transforms: { p: { type: "javascript", expr: "previous_result" } } } }] } }] };
  const fid = await start("/jobs/run/preview_flow", JSON.stringify({ value: flow, args: {} }));
  const fr = await done(fid);
  return {
    wr37b: await preview("export function main(...xs) { return xs.length; }"),
    wr51d: await preview("function tag(c: any) { return c; }\n@tag class Box {}\nexport function main() { return 1; }"),
    wr52a: await preview("export function main(x: unknown) { return (<number>x) + 1; }", { x: 1 }),
    wr52a_as: await preview("export function main(x: unknown) { return (x as number) + 1; }", { x: 1 }),
    wr52e: await preview("const id1 = <T>(x: T): T => x;\nconst id2 = <T,>(x: T) => x;\nconst id3 = <T extends unknown>(x: T) => x;\nexport function main() { return [id1(1), id2(2), id3(3)]; }"),
    je36d: { success: fr.success, result: fr.result },
  };
}
