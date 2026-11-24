// Step d, the driver: waits until the sibling w has set the flow user
// state key started (at most 60 s), then reads the run's root flow job and
// the flow jobs of p's branches (WA-19) to find the jobs of a, c and w. It
// polls each of them three ways (WA-V2, WA-15): get_result_maybe, the same
// with get_started=true, and get_result, keeping each answer's status,
// content type and body text. A stack names the worker's directory, which
// differs between workers, so each "stack" string in a body is written
// "(stack)". A not-found text ends with the parenthesised location of
// Windmill's source (" (jobs.rs:12065)"), which windmill-lite leaves out
// as a named deviation, so a body loses that location. The rest of the
// body keeps its spelling. Then it sets release, so that w returns, and
// returns the nine answers.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const root = e.WM_ROOT_JOB_ID!;
  const state = `${base}/jobs/flow/user_states/${root}`;
  const mask = (body: string) =>
    body.replace(/("stack":\s?)"(?:[^"\\]|\\.)*"/g, '$1"(stack)"').replace(/ \([^ ()]+\.rs:[0-9]+\)$/, "");
  const get = async (path: string) => {
    const res = await fetch(`${base}/${path}`, { headers: auth });
    return { status: res.status, type: res.headers.get("content-type"), body: mask(await res.text()) };
  };
  const job = async (id: string) => {
    const a = await get(`jobs_u/get/${id}?no_logs=true`);
    if (a.status !== 200) throw new Error(`job read ${id}: ${a.status} ${a.body}`);
    return JSON.parse(a.body);
  };
  let started = false;
  for (let i = 0; i < 600 && !started; i++) {
    started = (await (await fetch(`${state}/started`, { headers: auth })).json()) === true;
    if (!started) await Bun.sleep(100);
  }
  if (!started) throw new Error("timed out waiting for started");
  try {
    const mods = (await job(root)).flow_status.modules;
    const at = (id: string) => mods.find((m: any) => m.id === id);
    let w: string | undefined;
    for (const fj of at("p")?.flow_jobs ?? []) {
      const m = (await job(fj)).flow_status.modules.find((m: any) => m.id === "w");
      if (m?.job) w = m.job;
    }
    const jobs: Record<string, string | undefined> = { a: at("a")?.job, c: at("c")?.job, w };
    const out: Record<string, unknown> = {};
    for (const [site, id] of Object.entries(jobs)) {
      if (!id) {
        out[site] = "no job";
        continue;
      }
      out[site] = {
        maybe: await get(`jobs_u/completed/get_result_maybe/${id}`),
        started: await get(`jobs_u/completed/get_result_maybe/${id}?get_started=true`),
        result: await get(`jobs_u/completed/get_result/${id}`),
      };
    }
    return out;
  } finally {
    await fetch(`${state}/release`, {
      method: "POST",
      headers: { ...auth, "Content-Type": "application/json" },
      body: "true",
    });
  }
}
