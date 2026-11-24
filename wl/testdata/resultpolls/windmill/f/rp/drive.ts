// The driver: finds the jobs of the modules ids names in its run's flow
// status (the job read of WM_ROOT_JOB_ID), and polls each of them through
// get_result_maybe and get_result. It returns each answer's status and
// body, a long body by its length and its first characters, and, for the
// module record names, the result that the job's record holds.
export async function main(ids: string[], record?: string) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const base = `${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`;
  const read = async (path: string) => (await fetch(`${base}/${path}`, { headers: auth })).text();
  const get = async (path: string) => {
    const res = await fetch(`${base}/${path}`, { headers: auth });
    const body = await res.text();
    return { status: res.status, length: body.length, body: body.length > 200 ? body.slice(0, 80) : body };
  };
  const root = JSON.parse(await read(`jobs_u/get/${e.WM_ROOT_JOB_ID}?no_logs=true`));
  const job = (id: string) => root.flow_status.modules.find((m: { id: string }) => m.id === id)?.job;
  const out: Record<string, unknown> = {};
  for (const id of ids) {
    out[id] = {
      maybe: await get(`jobs_u/completed/get_result_maybe/${job(id)}`),
      result: await get(`jobs_u/completed/get_result/${job(id)}`),
    };
  }
  if (record) out.record = JSON.parse(await read(`jobs_u/get/${job(record)}?no_logs=true`)).result;
  return out;
}
