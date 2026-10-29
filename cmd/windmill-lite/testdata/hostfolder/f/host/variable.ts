// Reads a variable of the bundle through the job API at BASE_URL, the
// platform facade's origin, which a host-mode step gets in its
// environment (ADR 0007, WA-1, WA-2).
export async function main() {
  const base = process.env.BASE_URL;
  const r = await fetch(`${base}/api/w/${process.env.WM_WORKSPACE}/variables/get_value/f/host/greeting`, {
    headers: { Authorization: `Bearer ${process.env.WM_TOKEN}` },
  });
  return { status: r.status, value: await r.json(), base };
}
