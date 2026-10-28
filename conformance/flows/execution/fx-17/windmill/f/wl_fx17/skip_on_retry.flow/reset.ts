// Sets f/wl_fx17/flag through the REST API with the job's own token.
export async function main() {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const url = `${base}/api/w/${process.env.WM_WORKSPACE}/variables/update/f/wl_fx17/flag`;
  const res = await fetch(url, {
    method: "POST",
    headers: { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify({ value: "run" }),
  });
  if (!res.ok) throw new Error(`variable update failed: ${res.status}`);
  return "reset";
}
