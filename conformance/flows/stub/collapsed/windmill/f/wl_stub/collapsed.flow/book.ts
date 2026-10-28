// Books a slot in a calendar that only the flow's owners can reach. The
// alias resolves to Date, so Windmill hands main a Date (WR-8, WR-V3).
type When = Date;

export async function main(at: When, n: number) {
  const res = await fetch("http://calendar.invalid/book", {
    method: "POST",
    body: JSON.stringify({ at: at.toISOString(), n }),
  });
  return await res.json();
}
