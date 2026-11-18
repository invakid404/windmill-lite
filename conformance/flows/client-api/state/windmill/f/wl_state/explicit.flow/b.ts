// Reads the explicit path step a wrote, updates it and reads it again.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
  const p = "f/wl_state/explicit";
  const before = await (await fetch(`${api}/get_value_interpolated/${encodeURI(p)}`, { headers: auth })).json();
  const exists = await (await fetch(`${api}/exists/${encodeURI(p)}`, { headers: auth })).json();
  const u = await fetch(`${api}/update_value/${encodeURI(p)}`, {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: JSON.stringify({ value: { from: "b", saw: before } }),
  });
  await u.text();
  const after = await (await fetch(`${api}/get_value_interpolated/${encodeURI(p)}`, { headers: auth })).json();
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  return { before, exists, update: u.status, after };
}
