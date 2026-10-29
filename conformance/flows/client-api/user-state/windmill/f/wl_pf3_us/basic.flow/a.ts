// Step a of basic (WA-14): the root lookup the stock client's flow-state
// helpers make, then the state routes at that root, with fetch at BASE_URL
// and the step's own token. It sets k and order and reads k back, reads a
// key never set, and calls both routes at its own job, a script job.
export async function main() {
  const e = process.env;
  const call = async (method: string, path: string, body?: unknown) => {
    const headers: Record<string, string> = { Authorization: `Bearer ${e.WM_TOKEN}` };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const type = (res.headers.get("content-type") ?? "").split(";")[0];
    return { status: res.status, type, text: await res.text() };
  };
  const root = JSON.parse((await call("GET", `jobs_u/get_root_job_id/${e.WM_JOB_ID}`)).text);
  return {
    root_is_env: root === e.WM_ROOT_JOB_ID,
    set: await call("POST", `jobs/flow/user_states/${root}/k`, { n: 1 }),
    set_order: await call("POST", `jobs/flow/user_states/${root}/order`, { b: 1, a: 2, ab: 3 }),
    get: await call("GET", `jobs/flow/user_states/${root}/k`),
    missing: await call("GET", `jobs/flow/user_states/${root}/missing`),
    own_set: await call("POST", `jobs/flow/user_states/${e.WM_JOB_ID}/k`, 1),
    own_get: await call("GET", `jobs/flow/user_states/${e.WM_JOB_ID}/k`),
  };
}
