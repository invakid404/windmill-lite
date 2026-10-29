// Counts its attempts in the variable f/wl_probe/attempt (reset to 0
// before every run) and throws until the second.
export async function main() {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const vars = `${base}/api/w/${process.env.WM_WORKSPACE}/variables`;
  const headers = { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" };
  const got = await fetch(`${vars}/get_value/f/wl_probe/attempt`, { headers });
  if (!got.ok) throw new Error(`variable read failed: ${got.status}`);
  const n = Number(await got.json()) + 1;
  const put = await fetch(`${vars}/update/f/wl_probe/attempt`, { method: "POST", headers, body: JSON.stringify({ value: String(n) }) });
  if (!put.ok) throw new Error(`variable update failed: ${put.status}`);
  if (n < 2) throw new Error(`attempt ${n}`);
  return { attempt: n };
}
