// A state counter at the default state path, with the calls the stock
// client's getState and setState make: the interpolating read (404 is a
// state never written), then exists, then create with type state or
// update_value. Each iteration of the simple loop body shares one state
// path, so the count goes on across iterations.
export async function main(i: number) {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
  const p = e.WM_STATE_PATH!;
  const got = await fetch(`${api}/get_value_interpolated/${encodeURI(p)}`, { headers: auth });
  const text = await got.text();
  const n: number = got.status === 404 ? 0 : JSON.parse(text);
  const exists = await (await fetch(`${api}/exists/${encodeURI(p)}`, { headers: auth })).json();
  const json = { ...auth, "Content-Type": "application/json" };
  const w = exists
    ? await fetch(`${api}/update_value/${encodeURI(p)}`, { method: "POST", headers: json, body: JSON.stringify({ value: n + 1 }) })
    : await fetch(`${api}/create`, { method: "POST", headers: json, body: JSON.stringify({ path: p, value: n + 1, resource_type: "state" }) });
  return { i, path: p, read: got.status, n, exists, write: w.status, written: await w.text() };
}
