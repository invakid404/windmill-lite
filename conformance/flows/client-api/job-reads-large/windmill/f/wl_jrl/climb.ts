// The walk of the job-reads-large fixture: from the step's own job up its
// parent chain to the root, with GET jobs_u/get/{id} without no_logs, as a
// step that walks its ancestors does (WA-19). Each level is reduced to its
// id, parent_job and script_path, its argument cfg.name, its argument
// padding's length and ends, and a replaced argument map's reason.
const e = process.env;
async function get(id: string) {
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get/${id}`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  const text = await res.text();
  return res.status === 200 ? { status: 200, json: JSON.parse(text) } : { status: res.status, text };
}
function cut(v: unknown) {
  return typeof v === "string" ? { len: v.length, head: v.slice(0, 16), tail: v.slice(-16) } : v;
}
export async function main() {
  const levels: unknown[] = [];
  let id: string | undefined = e.WM_JOB_ID;
  for (let up = 0; id && up < 30; up++) {
    const a = await get(id);
    if (a.status !== 200) {
      levels.push(a);
      break;
    }
    const j = a.json;
    levels.push({
      id: j.id,
      parent_job: j.parent_job ?? null,
      script_path: j.script_path ?? null,
      name: j.args?.cfg?.name ?? null,
      padding: cut(j.args?.padding ?? null),
      reason: j.args?.reason ?? null,
    });
    id = j.parent_job;
  }
  return levels;
}
