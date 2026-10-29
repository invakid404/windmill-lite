// Step a of wait: it dispatches two scripts, as runScriptByPath does, and a
// flow, as runFlowAsync does, waits for each as waitJob does, and polls a
// UUID that names no job, with and without get_started (WA-15, WA-V2).
export async function main() {
  const e = process.env;
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  // The request runScriptByPathAsync (track true) and runFlowAsync make:
  // the arguments as the body, parent_job and root_job in the query when
  // the run is tracked in its parent (WA-15).
  const dispatch = async (kind: string, path: string, args: unknown, track: boolean) => {
    const q = new URLSearchParams();
    if (track) {
      q.set("parent_job", e.WM_JOB_ID!);
      if (e.WM_ROOT_FLOW_JOB_ID) q.set("root_job", e.WM_ROOT_FLOW_JOB_ID);
    }
    const res = await fetch(`${base}/jobs/run/${kind}/${path}?${q}`, {
      method: "POST",
      headers: { ...auth, "Content-Type": "application/json" },
      body: JSON.stringify(args),
    });
    return { status: res.status, id: await res.text() };
  };
  // waitJob: poll get_result_maybe until the job has completed (WA-15).
  const wait = async (id: string) => {
    for (let i = 0; i < 1200; i++) {
      const v = await (await fetch(`${base}/jobs_u/completed/get_result_maybe/${id}`, { headers: auth })).json();
      if (v.completed) return v;
      await new Promise((r) => setTimeout(r, 100));
    }
    throw new Error(`job ${id} did not complete`);
  };
  const add = await dispatch("p", "f/wl_pf3_disp/add", { x: 2 }, true);
  const added = await wait(add.id);
  const fail = await dispatch("p", "f/wl_pf3_disp/fail", {}, true);
  const failed = await wait(fail.id);
  const flow = await dispatch("f", "f/wl_pf3_disp/child", { v: 4 }, false);
  const flowed = await wait(flow.id);
  const unknown = "00000000-0000-4000-8000-000000000000";
  const poll = async (q: string) => await (await fetch(`${base}/jobs_u/completed/get_result_maybe/${unknown}${q}`, { headers: auth })).json();
  return {
    add: { status: add.status, started: added.started, success: added.success, result: added.result },
    fail: { status: fail.status, success: failed.success, name: failed.result.error.name, message: failed.result.error.message },
    flow: { status: flow.status, success: flowed.success, result: flowed.result },
    unknown: await poll(""),
    unknown_started: await poll("?get_started=true"),
  };
}
