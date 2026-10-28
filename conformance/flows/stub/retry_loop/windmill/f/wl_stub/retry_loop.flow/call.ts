// Reserves one item in an inventory service that only the flow's owners
// can reach. On the oracle the stub answers instead.
export async function main(item: string) {
  const res = await fetch(`http://inventory.invalid/reserve/${encodeURIComponent(item)}`, { method: "POST" });
  if (!res.ok) throw new Error(`inventory: HTTP ${res.status}`);
  return await res.json();
}
