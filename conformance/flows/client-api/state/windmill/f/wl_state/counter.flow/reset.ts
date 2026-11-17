// Deletes the loop body's state, so every run of the counter starts from a
// state never written: the body's state path is the flow's path, the loop's
// id and the trigger (WA-5, WA-6/a).
export async function main() {
  const e = process.env;
  const p = `${e.WM_FLOW_PATH}/l_user`;
  const r = await fetch(`${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources/delete/${encodeURI(p)}`, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
  });
  await r.text();
  return p;
}
