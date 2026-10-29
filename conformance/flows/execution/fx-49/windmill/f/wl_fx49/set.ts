// Sets the variable f/wl_fx49/<name> to "yes" in the first iteration
// (v is 1) and to "no" after it, through the REST API, then returns v.
export async function main(name: string, v: number) {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const url = `${base}/api/w/${process.env.WM_WORKSPACE}/variables/update/f/wl_fx49/${name}`;
  const res = await fetch(url, {
    method: "POST",
    headers: { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify({ value: v == 1 ? "yes" : "no" }),
  });
  if (!res.ok) throw new Error(`variable update failed: ${res.status}`);
  return v;
}
