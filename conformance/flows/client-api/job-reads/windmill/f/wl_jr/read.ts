// The reading step of the job-reads fixture. It reads the job tree of its
// own run with GET jobs_u/get/{id}?no_logs=true, as a step that reads its
// own job tree does (WA-19): its own job, each ancestor up to the run's
// root flow, then each job that the statuses of the root and its child
// flows name, depth first. Each answer is reduced to the fields that
// windmill-lite serves (pick); extra lists every other field that is not
// known to be left out.
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
function keep(v: any, served: Set<string>, left: Set<string>, where: string, each: (k: string, x: any) => any): any {
  const out: Record<string, unknown> = {};
  for (const [k, x] of Object.entries(v)) {
    if (served.has(k)) out[k] = each(k, x);
    else if (!left.has(k)) extra.add(`${where}.${k}`);
  }
  return out;
}
function entry(m: any): any {
  return m === null || typeof m !== "object" ? m : keep(m, MODULE_SERVED, MODULE_LEFT_OUT, "module", (_, x) => x);
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
export async function main() {
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
    if (m.job) await visit(m.job, site);
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
