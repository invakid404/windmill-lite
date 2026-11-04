// Step p of answers: the answers of GET jobs_u/get/{id} and of GET
// jobs_u/get_args/{id} for a UUID that names no job and for an id that is
// not a UUID, and the answers for step
// a's completed job (which wrote a log line, and whose result's text it
// reads back) and for this running job
// without no_logs and with no_code, each reduced to the fields that
// windmill-lite serves (pick). A location suffix that Windmill writes in
// parentheses at the end of a not-found text is cut, since windmill-lite
// writes none (D14).
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
async function args(id: string) {
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get_args/${id}`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  return { status: res.status, type: res.headers.get("content-type"), text: await res.text() };
}
export async function main() {
  const unknown = "00000000-0000-4000-8000-000000000000";
  const plain = (a: any) => ({ status: a.status, type: a.type, text: a.text.replaceAll(unknown, "<id>").replace(/ \(\w+\.rs:\d+\)$/, "") });
  const own = await get(e.WM_JOB_ID!);
  const a = (await get(own.json.parent_job)).json.flow_status.modules[0].job;
  return {
    unknown: plain(await get(unknown)),
    unknown_with_logs: plain(await get(unknown, "")),
    not_uuid: plain(await get("abc")),
    a: pick((await get(a)).json),
    // How the record spells a's result: the text of its array k.
    a_k: (await get(a)).text.match(/"k":\s?\[[^\]]*\]/)?.[0] ?? null,
    a_with_logs: pick((await get(a, "")).json),
    a_no_logs_false: pick((await get(a, "?no_logs=false")).json),
    a_no_code: pick((await get(a, "?no_logs=true&no_code=true")).json),
    own_with_logs: pick((await get(e.WM_JOB_ID!, "")).json),
    args_unknown: plain(await args(unknown)),
    args_not_uuid: plain(await args("abc")),
    extra: [...extra].sort(),
  };
}
