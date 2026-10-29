// Holds an order's items in an inventory service that only the flow's
// owners can reach. On the oracle the stub answers instead.
export async function main(order: string) {
  const res = await fetch(`http://inventory.invalid/hold/${encodeURIComponent(order)}`, { method: "POST" });
  if (!res.ok) throw new Error(`inventory: HTTP ${res.status}`);
  return await res.json();
}
