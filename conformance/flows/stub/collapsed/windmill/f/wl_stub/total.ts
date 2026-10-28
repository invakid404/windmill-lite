// Adds the booked slots up in a ledger that only the flow's owners can reach.
export async function main(slots: number[]) {
  const res = await fetch("http://ledger.invalid/total", { method: "POST", body: JSON.stringify(slots) });
  return await res.json();
}
