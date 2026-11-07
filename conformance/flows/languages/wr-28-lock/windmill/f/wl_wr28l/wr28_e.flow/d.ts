// Talks to the oracle's API with the step's own token (a black-box client,
// as the harness is), so a recording can hold what only the API shows: the
// dependency job of a script created at run time.
const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
const ws = process.env.WM_WORKSPACE!;
const root = process.env.WM_ROOT_FLOW_JOB_ID!;
const auth = { Authorization: `Bearer ${process.env.WM_TOKEN}` };
type Answer = { status: number; body: string };
async function call(method: string, path: string, body?: string): Promise<Answer> {
  const headers: Record<string, string> = { ...auth };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const r = await fetch(`${base}/api/w/${ws}${path}`, { method, headers, body });
  return { status: r.status, body: await r.text() };
}
async function json(method: string, path: string, value?: unknown): Promise<any> {
  const a = await call(method, path, value === undefined ? undefined : JSON.stringify(value));
  if (a.status >= 300) throw new Error(`${method} ${path}: ${a.status} ${a.body}`);
  return a.body === "" ? null : JSON.parse(a.body);
}
const sleep = (ms: number) => new Promise((res) => setTimeout(res, ms));
// A driver reads the oracle's API: anywhere else (windmill-lite's facade
// answers only the routes a step's client makes) it fails at once.
async function oracleAPI() {
  const r = await fetch(`${base}/api/version`);
  const v = r.ok ? await r.text() : "";
  if (!v.startsWith("CE v")) throw new Error(`the driver needs Windmill's API; /api/version answered ${r.status} ${v}`);
}
// Each repeat of the run creates its own scripts.
const tag = () => root.replaceAll("-", "").slice(0, 12);

// Creates a Bun script and waits for its dependency job; returns the job's
// outcome and the script's deployment status afterwards.
async function deploy(path: string, content: string) {
  const created = await call("POST", "/scripts/create", JSON.stringify({ path, summary: "WR-28/e", description: "", content, language: "bun" }));
  if (created.status >= 300) throw new Error(`scripts/create ${path}: ${created.status} ${created.body}`);
  const hash = created.body.trim().replaceAll('"', "");
  let deps: any[] = [];
  for (let i = 0; i < 400 && deps.length === 0; i++) {
    deps = await json("GET", `/jobs/completed/list?script_path_exact=${encodeURIComponent(path)}&job_kinds=dependencies`);
    if (deps.length === 0) await sleep(250);
  }
  if (deps.length === 0) throw new Error(`no dependency job of ${path} completed`);
  const r = await json("GET", `/jobs_u/completed/get_result_maybe/${deps[0].id}`);
  const st = await json("GET", `/scripts/deployment_status/h/${hash}`);
  const message: string = r?.result?.error?.message ?? "";
  return {
    dependency_success: r.success,
    locked: st.lock !== null,
    lock_error_logs: typeof st.lock_error_logs === "string" && st.lock_error_logs !== "",
    lines: message.split("\n").filter((l) => l === "execution error:" || l.startsWith("Error locking file:") || l.startsWith("error:")),
  };
}

export async function main() {
  await oracleAPI();
  const t = tag();
  const dir = "f/wl_wr28l";
  const helper = await deploy(`${dir}/helper_${t}`, `export function h() {\n  return "helper";\n}\nexport function main() {\n  return h();\n}\n`);
  if (!helper.dependency_success) throw new Error(`the helper did not lock: ${JSON.stringify(helper)}`);
  // WR-28/e: the static ./helper_<tag> is a substring of the dynamic
  // specifier, which the loader no longer rewrites.
  const sub = await deploy(`${dir}/x_dyn_sub_${t}`, `import { h } from "./helper_${t}";\nexport async function main() {\n  const m = await import("../wl_wr28c/./helper_${t}");\n  return [h(), m.h()];\n}\n`);
  // WR-28/c's shape: the identical dynamic literal is rewritten with the
  // static one.
  const same = await deploy(`${dir}/x_dyn_same_${t}`, `import { h } from "./helper_${t}";\nexport async function main() {\n  const m = await import("./helper_${t}");\n  return [h(), m.h()];\n}\n`);
  return { sub, same };
}
