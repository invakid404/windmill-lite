// Deletes the count the loop left and checks that it is gone, so the run
// leaves nothing in the workspace.
export async function main() {
  const e = process.env;
  const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
  const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
  const p = `${e.WM_FLOW_PATH}/l_user`;
  const before = await (await fetch(`${api}/get_value/${encodeURI(p)}`, { headers: auth })).text();
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  const exists = await (await fetch(`${api}/exists/${encodeURI(p)}`, { headers: auth })).json();
  return { before, exists };
}
