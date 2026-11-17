// An explicit state path that two steps share: step a deletes it, writes
// it with the calls setState(v, path) makes (exists, then create with type
// state), and step b reads it with getState(path)'s call, then updates it.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
  const p = "f/wl_state/explicit";
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  const exists = await (await fetch(`${api}/exists/${encodeURI(p)}`, { headers: auth })).json();
  const w = await fetch(`${api}/create`, {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: JSON.stringify({ path: p, value: { from: "a" }, resource_type: "state" }),
  });
  return { exists, create: w.status, own_path_differs: e.WM_STATE_PATH !== p };
}
