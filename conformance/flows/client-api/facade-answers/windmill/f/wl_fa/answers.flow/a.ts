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

export async function main(completed: string) {
  await oracleAPI();
  const self = process.env.WM_JOB_ID!;
  const scrub = (a: Answer) => ({ ...a, body: a.body.replaceAll(completed, "<completed>").replaceAll(self, "<self>").replaceAll(root, "<root>") });
  const out: Record<string, unknown> = {};
  out.wa19b = await call("GET", "/jobs_u/get_root_job_id/00000000-0000-0000-0000-000000000001");
  out.wa19c = await call("GET", "/jobs_u/get_root_job_id/abc");
  out.wav2c = await call("GET", "/jobs_u/completed/get_result_maybe/abc");
  out.wa14d = [
    await call("GET", "/jobs/flow/user_states/abc/k"),
    await call("POST", `/jobs/flow/user_states/${root}/k`, "hello", "application/json"),
    await call("POST", `/jobs/flow/user_states/${root}/k`, "hello", null),
  ].map(scrub);
  // WA-11/e: a variable's create and update answers, then the write a
  // setVariable makes and a read of it.
  await call("DELETE", "/variables/delete/f/wl_fa/w");
  const w = (value: string) => JSON.stringify({ path: "f/wl_fa/w", value, is_secret: false, description: "" });
  out.wa11e = [
    await call("POST", "/variables/create", w("a")),
    await call("POST", "/variables/create", w("a")),
    await call("POST", "/variables/update/f/wl_fa/w", JSON.stringify({ value: "b" })),
    await call("POST", "/variables/update/f/wl_fa/none", JSON.stringify({ value: "b" })),
    await call("POST", "/variables/update/f/wl_fa/w", JSON.stringify({ value: "c" })),
    await call("GET", "/variables/get_value/f/wl_fa/w"),
  ];
  // WA-11/c: secrets stored as ciphertext under another key; the first
  // 16-byte candidate whose read fails with the padding cause is kept.
  const cands = ["AAAAAAAAAAAAAAAAAAAAAA==", "AQEBAQEBAQEBAQEBAQEBAQ==", "AgICAgICAgICAgICAgICAg=="];
  const secret = (path: string, value: string) => JSON.stringify({ path, value, is_secret: true, description: "" });
  let kept = -1, read: Answer | null = null;
  for (let i = 0; i < cands.length && kept < 0; i++) {
    await call("DELETE", "/variables/delete/f/wl_fa/s");
    const c = await call("POST", "/variables/create?already_encrypted=true", secret("f/wl_fa/s", cands[i]));
    read = await call("GET", "/variables/get_value/f/wl_fa/s");
    if (c.status < 300 && read.body.includes("Could not decrypt value")) kept = i;
  }
  out.wa11c = { kept, read };
  // WA-11/d: the shape check.
  const shape: Answer[] = [];
  for (const v of ["hunter2", "dGVzdA==", "$vault:f/wl_fa/p"]) {
    await call("DELETE", "/variables/delete/f/wl_fa/p");
    shape.push(await call("POST", "/variables/create?already_encrypted=true", secret("f/wl_fa/p", v)));
  }
  shape.push(await call("GET", "/variables/get_value/f/wl_fa/p"));
  out.wa11d = shape;
  // WA-12/g: job_id on the interpolated resource read.
  const rnd = crypto.randomUUID();
  const ipol = (p: string, id: string) => call("GET", `/resources/get_value_interpolated/${p}?job_id=${id}`);
  out.wa12g = {
    plain: [await ipol("f/wl_fa/plain", rnd), await ipol("f/wl_fa/plain", completed), await ipol("f/wl_fa/plain", self)].map((a) => ({ ...a, body: a.body.replaceAll(rnd, "<random>") })).map(scrub),
    ctx: [await ipol("f/wl_fa/ctx", rnd), await ipol("f/wl_fa/ctx", completed), await ipol("f/wl_fa/ctx", self)].map((a) => ({ ...a, body: a.body.replaceAll(rnd, "<random>") })).map(scrub),
    nope: [await ipol("f/wl_fa/plain", "nope"), await ipol("f/wl_fa/ctx", "nope")].map((a) => a.status),
  };
  // WA-12/h: CUSTOM_INSTANCE_DB/ as the superadmin, connection fields masked.
  const cdb = await call("GET", "/resources/get_value_interpolated/CUSTOM_INSTANCE_DB/demo");
  let cdbValue: any = null;
  try {
    cdbValue = JSON.parse(cdb.body);
    for (const k of ["host", "user", "password", "port", "sslmode"]) if (k in cdbValue) cdbValue[k] = "<masked>";
  } catch { cdbValue = cdb.body; }
  out.wa12h = { interpolated: { status: cdb.status, value: cdbValue }, raw: await call("GET", "/resources/get_value/CUSTOM_INSTANCE_DB/demo") };
  return out;
}
