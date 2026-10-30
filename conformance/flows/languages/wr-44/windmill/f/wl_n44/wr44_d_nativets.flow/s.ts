// Sets the instance setting job_default_timeout through the API with
// the step's own token (the oracle's superadmin), or removes it.
export async function main(value: number | null) {
  const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
  const res = await fetch(`${base}/api/settings/global/job_default_timeout`, {
    method: "POST", headers: { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify({ value }),
  });
  return res.status;
}
