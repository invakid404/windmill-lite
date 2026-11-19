// Reads the counter at f/wlt_iso/state (0 when it does not exist), and
// writes it back one higher: a create the first time, an update after.
export async function main() {
  const base = `${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/resources`;
  const headers = { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" };
  const path = "f/wlt_iso/state";
  const got = await fetch(`${base}/get_value/${path}`, { headers });
  if (got.status !== 200 && got.status !== 404) throw new Error(`read: ${got.status} ${await got.text()}`);
  const n = got.status === 404 ? 0 : ((await got.json()) as number);
  const written =
    n === 0
      ? await fetch(`${base}/create`, { method: "POST", headers, body: JSON.stringify({ path, value: 1, resource_type: "state" }) })
      : await fetch(`${base}/update_value/${path}`, { method: "POST", headers, body: JSON.stringify({ value: n + 1 }) });
  if (!written.ok) throw new Error(`write: ${written.status} ${await written.text()}`);
  return { n: n + 1 };
}
