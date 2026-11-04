// Step r: reads, with GET jobs_u/get/{id}?no_logs=true, the job of step v,
// whose result is large, and the job of step w, whose argument x is, as
// the flow's status names them (WA-19). A string is reduced to its length
// and its first and last 16 characters, an array to its JSON text's length
// and an FNV-1a hash of that text; any other value is kept.
const e = process.env;
async function get(id: string) {
  const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get/${id}?no_logs=true`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  const text = await res.text();
  return res.status === 200 ? { status: 200, json: JSON.parse(text) } : { status: res.status, text };
}
function fnv(t: string) {
  let h = 0x811c9dc5;
  for (let i = 0; i < t.length; i++) h = Math.imul(h ^ t.charCodeAt(i), 0x01000193) >>> 0;
  return h;
}
function cut(v: unknown) {
  if (typeof v === "string") return { len: v.length, head: v.slice(0, 16), tail: v.slice(-16) };
  if (Array.isArray(v)) {
    const t = JSON.stringify(v);
    return { json: t.length, fnv: fnv(t) };
  }
  return v;
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
