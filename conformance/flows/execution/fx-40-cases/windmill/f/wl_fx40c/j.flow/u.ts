export async function main(v: string) {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const res = await fetch(`${base}/api/w/${process.env.WM_WORKSPACE}/variables/update/f/wl_fx40c/v`, {
    method: "POST", headers: { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify({ value: "b" }),
  });
  return res.status;
}
