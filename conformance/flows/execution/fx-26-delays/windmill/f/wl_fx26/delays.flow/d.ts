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

// Starts the flow, waits until its step lists `which` failed attempts and
// names a queued retry job, reads that job's scheduled_for, subtracts the
// last failed attempt's completion, and cancels the flow.
async function delay(path: string, which: number): Promise<number> {
  const id = await start(`/jobs/run/f/${path}`, "{}");
  for (let i = 0; i < 1600; i++) {
    const m = (await json("GET", `/jobs_u/get/${id}`)).flow_status?.modules?.[0];
    const failed: string[] = m?.failed_retries ?? [];
    if (failed.length >= which && m.job && !failed.includes(m.job)) {
      const retry = await json("GET", `/jobs_u/get/${m.job}`);
      if (retry.scheduled_for) {
        const last = await json("GET", `/jobs_u/completed/get/${failed[which - 1]}`);
        const end = last.completed_at ? Date.parse(last.completed_at) : Date.parse(last.started_at) + last.duration_ms;
        await call("POST", `/jobs_u/queue/cancel/${id}`, JSON.stringify({ reason: "wltest: FX-26 delay read" }));
        return Math.round((Date.parse(retry.scheduled_for) - end) / 1000);
      }
    }
    await sleep(250);
  }
  throw new Error(`no retry job for ${path}`);
}
export async function main() {
  const f = await Promise.all(Array.from({ length: 20 }, () => delay("f/wl_fx26/retry_f", 1)));
  return {
    c: await delay("f/wl_fx26/retry_c", 1),
    d: await delay("f/wl_fx26/retry_d", 1),
    e_second: await delay("f/wl_fx26/retry_e", 2),
    f: { count: f.length, all_in_6_to_14: f.every((d) => d >= 6 && d <= 14) },
  };
}
