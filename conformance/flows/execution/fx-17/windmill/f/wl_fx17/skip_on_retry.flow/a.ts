// Sets f/wl_fx17/flag to "skip" through the REST API, then throws, so the
// retry's skip_if sees the new value.
export async function main(x: number) {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const url = `${base}/api/w/${process.env.WM_WORKSPACE}/variables/update/f/wl_fx17/flag`;
  const res = await fetch(url, {
    method: "POST",
    headers: { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify({ value: "skip" }),
  });
  if (!res.ok) throw new Error(`variable update failed: ${res.status}`);
  throw new Error(`attempt with x=${x}`);
}
