// Confirms an order in an order service that only the flow's owners can
// reach.
export async function main(order: string, hold: object) {
  const res = await fetch(`http://orders.invalid/confirm/${encodeURIComponent(order)}`, { method: "POST", body: JSON.stringify(hold) });
  if (!res.ok) throw new Error(`orders: HTTP ${res.status}`);
  return await res.json();
}
