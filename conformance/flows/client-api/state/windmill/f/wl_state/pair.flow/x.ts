// A counter at the default state path across separate runs of the flow,
// with the calls the stock client's getState and setState make: the
// interpolating read (404 is a state never written), then exists, then
// create with type state or update_value. The fixture's runs take the
// phases in name order:
// - "first" deletes the state, then counts: it reads nothing (0) and
//   creates 1;
// - "second" counts: it reads 1, written by the earlier run, and updates
//   to 2; it then puts 1 back, so that the oracle's repetition of this run
//   reads 1 again;
// - "cleanup" deletes the state and checks that it is gone.
export async function main(phase: string) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const json = { ...auth, "Content-Type": "application/json" };
  const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
  const p = e.WM_STATE_PATH!;
  const del = async () => { await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text(); };
  const exists = async () => await (await fetch(`${api}/exists/${encodeURI(p)}`, { headers: auth })).json();
  if (phase === "cleanup") {
    await del();
    return { phase, path: p, exists: await exists() };
  }
  if (phase === "first") await del();
  const got = await fetch(`${api}/get_value_interpolated/${encodeURI(p)}`, { headers: auth });
  const text = await got.text();
  const n: number = got.status === 404 ? 0 : JSON.parse(text);
  const found = await exists();
  const w = found
    ? await fetch(`${api}/update_value/${encodeURI(p)}`, { method: "POST", headers: json, body: JSON.stringify({ value: n + 1 }) })
    : await fetch(`${api}/create`, { method: "POST", headers: json, body: JSON.stringify({ path: p, value: n + 1, resource_type: "state" }) });
  const out: Record<string, unknown> = { phase, path: p, read: got.status, n, exists: found, write: w.status, written: await w.text() };
  if (phase === "second") {
    const back = await fetch(`${api}/update_value/${encodeURI(p)}`, { method: "POST", headers: json, body: JSON.stringify({ value: n }) });
    await back.text();
    out.put_back = back.status;
  }
  return out;
}
