// The get_args probe: GET jobs_u/get_args/{id} on the step's own job, as
// JobService.getJobArgs requests it, and on the flow job it runs in
// (WA-19). Each answer is kept as its status, its content type and its
// text, and label is read back from the step's own arguments; k is how
// GET jobs_u/get/{id} spells a nested argument.
export async function main(label: string, cfg: object) {
  const e = process.env;
  const get = async (id: string) => {
    const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get_args/${id}`, {
      headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
    });
    return { status: res.status, type: res.headers.get("content-type"), text: await res.text() };
  };
  const own = await get(e.WM_JOB_ID!);
  // How GET jobs_u/get/{id} writes a nested argument: the text of cfg's
  // array k, as the answer spells it.
  const job = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/jobs_u/get/${e.WM_JOB_ID}?no_logs=true`, {
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  const k = (await job.text()).match(/"k":\s?\[[^\]]*\]/)?.[0] ?? null;
  return { own, label: own.status === 200 ? JSON.parse(own.text).label : null, flow: await get(e.WM_FLOW_JOB_ID!), k };
}
