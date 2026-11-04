// Step r: reads, with GET jobs_u/get/{id}?no_logs=true, the job of step v,
// whose result is large, and the job of step w, whose argument x is, as
// the flow's status names them (WA-19). A string is reduced to its length
// and its first and last 16 characters; any other value is kept.
const e = process.env;
async function get(id: string) {
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get/${id}?no_logs=true`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  const text = await res.text();
  return res.status === 200 ? { status: 200, json: JSON.parse(text) } : { status: res.status, text };
}
function cut(v: unknown) {
  return typeof v === "string" ? { len: v.length, head: v.slice(0, 16), tail: v.slice(-16) } : v;
}
export async function main() {
  const flow = await get(e.WM_FLOW_JOB_ID!);
  const [v, w] = flow.json.flow_status.modules;
  const a = await get(v.job);
  const b = await get(w.job);
  return {
    v: a.status === 200 ? { result: cut(a.json.result) } : a,
    w: b.status === 200 ? { args: b.json.args?.x === undefined ? b.json.args : { x: cut(b.json.args.x) } } : b,
  };
}
