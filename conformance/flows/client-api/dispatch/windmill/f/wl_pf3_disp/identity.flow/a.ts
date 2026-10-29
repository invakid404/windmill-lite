// Step a of identity: it dispatches the probe script tracked in its parent,
// as runScriptByPath does, and returns how the probe's job identity
// relates to its own, without the ids themselves: what WM_FLOW_JOB_ID,
// WM_ROOT_JOB_ID and the root lookup of a dispatched job are is not in the
// reference (WA-V1 leaves dispatched jobs out), so the oracle records it.
// It also returns the keys of a dispatched failure's error.
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
  const relate = (p: any) => ({
    flow_job_is_mine: p.flow_job === e.WM_JOB_ID,
    flow_job_is_my_flow: p.flow_job === e.WM_FLOW_JOB_ID,
    flow_job_empty: p.flow_job === "",
    root_is_mine: p.root === e.WM_ROOT_JOB_ID,
    root_is_itself: p.root === p.job,
    root_is_my_job: p.root === e.WM_JOB_ID,
    lookup_is_root: p.lookup === p.root,
    root_flow_empty: p.root_flow === "",
    root_flow_is_mine: p.root_flow === e.WM_ROOT_FLOW_JOB_ID,
    flow_path: p.flow_path,
    job_path: p.job_path,
    step: p.step,
    state_path: p.state_path,
  });
  const tracked = await wait((await dispatch("p", "f/wl_pf3_disp/probe", {}, true)).id);
  const fail = await wait((await dispatch("p", "f/wl_pf3_disp/fail", {}, true)).id);
  return {
    tracked: relate(tracked.result),
    fail_keys: Object.keys(fail.result.error).sort(),
    fail_top: Object.keys(fail.result).sort(),
  };
}
