// A child of a parallel container in the job-reads-parallel fixture. Two
// children run at once on two workers and complete in the reverse of
// their order, told apart by role, and "fast" returns at once: "slow" sets the flow user state key
// <k>_started, waits until the reader sets <k>_read, then waits 1.5 s
// more, so that it completes last; "read" waits until <k>_started is set,
// reads the job tree of its run as f/wl_jrp/read does, sets <k>_read and
// returns what it read. "readnow" reads at once; "fast2" sets <k>_f2 and
// returns; "read2" waits until <k>_f2 is set and a second more, then
// reads; "fastmark" sets <k>_fast and returns; "readafter" waits until
// <k>_fast is set and a second more, then reads; "readwaits" waits until
// <k>_a and <k>_f are set and 1.5 s more, then reads; "readwaits2" the
// same for <k>_a, <k>_c, <k>_d, <k>_sf and <k>_l. The keys are the run's root flow job's (WA-14).
const NIL = "00000000-0000-0000-0000-000000000000";
const e = process.env;
// The fields of a job that windmill-lite serves (WA-19), and those it
// leaves out. pick keeps the served ones; every other field goes to extra,
// unless it is one that is left out, so a field Windmill adds shows.
const SERVED = new Set([
  "type", "id", "parent_job", "root_job", "running", "flow_step_id", "canceled",
  "success", "result", "args", "script_path", "is_flow_step", "language",
  "workspace_id", "created_by", "email", "permissioned_as", "flow_status",
]);
const LEFT_OUT = new Set([
  "created_at", "started_at", "completed_at", "scheduled_for", "last_ping",
  "duration_ms", "self_wait_time_ms", "aggregate_wait_time_ms", "worker",
  "mem_peak", "script_hash", "raw_code", "raw_lock", "raw_flow", "logs",
  "job_kind", "tag", "priority", "visible_to_owner", "is_retry", "same_worker",
  "suspend", "script_entrypoint_override", "result_columns", "deleted",
  "is_skipped", "leaf_jobs", "canceled_by", "canceled_reason", "schedule_path",
  "workflow_as_code_status", "preprocessed", "labels",
]);
const STATUS_SERVED = new Set(["step", "modules", "failure_module", "preprocessor_module", "cleanup_module", "user_states"]);
const STATUS_LEFT_OUT = new Set(["retry"]);
const MODULE_SERVED = new Set([
  "id", "type", "job", "skipped", "flow_jobs", "flow_jobs_success", "branch_chosen",
  "branchall", "failed_retries", "parent_module", "iterator", "parallel", "while_loop",
]);
const MODULE_LEFT_OUT = new Set(["flow_jobs_duration", "count", "progress", "approvers", "agent_actions", "agent_actions_success"]);
// The fields whose values are the job's identity in the environment: the
// workspace and the user the run is permissioned as (OR-29).
const ENV: Record<string, string> = {
  workspace_id: "WM_WORKSPACE", created_by: "WM_USERNAME", email: "WM_EMAIL", permissioned_as: "WM_PERMISSIONED_AS",
};
const extra = new Set<string>();
// A running parallel container names as its job the child that a worker
// picked up last, whose order varies between runs when children are
// released together: always in a parallel branchall, and in a parallel
// loop at its start. The reader writes such a job as <racy> and does not
// walk from it (WA-19/n): a running parallel branchall's always, and a
// running parallel loop's while racy is set, by readTree's racyLoops on
// every read, for the reader of a loop whose children start together.
let racy = false;
const racyLoop = (m: any) =>
  m?.parallel === true && (m?.branchall !== undefined || (racy && m?.iterator !== undefined));
function keep(v: any, served: Set<string>, left: Set<string>, where: string, each: (k: string, x: any) => any): any {
  const out: Record<string, unknown> = {};
  for (const [k, x] of Object.entries(v)) {
    if (served.has(k)) out[k] = each(k, x);
    else if (!left.has(k)) extra.add(`${where}.${k}`);
  }
  return out;
}
function entry(m: any): any {
  if (m === null || typeof m !== "object") return m;
  return keep(m, MODULE_SERVED, MODULE_LEFT_OUT, "module", (k, x) => (k === "job" && racyLoop(m) ? "<racy>" : x));
}
function pick(job: any): any {
  return keep(job, SERVED, LEFT_OUT, "job", (k, x) => {
    if (ENV[k]) return x === e[ENV[k]] ? `<env:${ENV[k]}>` : x;
    if (k !== "flow_status" || x === null) return x;
    return keep(x, STATUS_SERVED, STATUS_LEFT_OUT, "flow_status", (s, y) =>
      s === "modules" ? y.map(entry) : s === "failure_module" || s === "preprocessor_module" ? entry(y) : y);
  });
}
async function get(id: string, q = "?no_logs=true") {
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get/${id}${q}`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  const text = await res.text();
  let json: any = undefined;
  if (res.status === 200) json = JSON.parse(text);
  return { status: res.status, type: res.headers.get("content-type"), text, json };
}
async function readTree(racyLoops = false) {
  racy = racyLoops;
  const out: Record<string, unknown> = {};
  const seen = new Set<string>();
  const record = async (id: string, site: string) => {
    const a = await get(id);
    out[site] = a.status === 200 ? pick(a.json) : { status: a.status, type: a.type, text: a.text };
    seen.add(id);
    return a.json;
  };
  const visit = async (id: string, site: string): Promise<void> => {
    if (id === NIL || seen.has(id)) return;
    const j = await record(id, site);
    if (j?.flow_status) await walk(j.flow_status, site);
  };
  const mod = async (m: any, site: string): Promise<void> => {
    if (m.job && !racyLoop(m)) await visit(m.job, site);
    const fj: string[] = m.flow_jobs ?? [];
    for (let i = 0; i < fj.length; i++) await visit(fj[i], `${site}/${i}`);
    const fr: string[] = m.failed_retries ?? [];
    for (let i = 0; i < fr.length; i++) await visit(fr[i], `${site}/r${i}`);
  };
  const walk = async (fs: any, site: string): Promise<void> => {
    for (const m of fs.modules ?? []) await mod(m, `${site}/${m.id}`);
    if (fs.failure_module?.job) await mod(fs.failure_module, `${site}/failure`);
    if (fs.preprocessor_module?.job) await mod(fs.preprocessor_module, `${site}/preprocessor`);
  };
  let cur = await record(e.WM_JOB_ID!, "own");
  for (let up = 1; cur?.parent_job; up++) cur = await record(cur.parent_job, `up${up}`);
  if (cur?.flow_status) await walk(cur.flow_status, "top");
  out.extra = [...extra].sort();
  return out;
}

async function state(key: string, value?: unknown): Promise<unknown> {
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = await (await fetch(`${base}/jobs_u/get_root_job_id/${e.WM_JOB_ID}`, { headers: auth })).json();
  const url = `${base}/jobs/flow/user_states/${root}/${key}`;
  if (value === undefined) return await (await fetch(url, { headers: auth })).json();
  await fetch(url, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: JSON.stringify(value) });
}
async function until(key: string) {
  for (let i = 0; i < 600; i++) {
    if ((await state(key)) !== null) return;
    await Bun.sleep(100);
  }
  throw new Error(`timed out waiting for ${key}`);
}
export async function main(role: string, k: string) {
  if (role === "fast") return "fast";
  if (role === "fastmark") {
    await state(`${k}_fast`, true);
    return "fast";
  }
  if (role === "readafter") {
    await until(`${k}_fast`);
    await Bun.sleep(1000);
    return await readTree();
  }
  if (role === "readwaits2") {
    for (const w of ["a", "c", "d", "sf", "l"]) await until(`${k}_${w}`);
    await Bun.sleep(1500);
    return await readTree();
  }
  if (role === "readwaits") {
    await until(`${k}_a`);
    await until(`${k}_f`);
    await Bun.sleep(1500);
    return await readTree();
  }
  if (role === "readnow") return await readTree();
  if (role === "fast2") {
    await state(`${k}_f2`, true);
    return "fast2";
  }
  if (role === "read2") {
    await until(`${k}_f2`);
    await Bun.sleep(1000);
    return await readTree();
  }
  if (role === "slow") {
    await state(`${k}_started`, true);
    await until(`${k}_read`);
    await Bun.sleep(1500);
    return "slow";
  }
  await until(`${k}_started`);
  await Bun.sleep(300);
  const out = await readTree(k === "l");
  await state(`${k}_read`, true);
  return out;
}
