// Confirms an order's holds in an order system that only the flow's owners
// can reach: a codebase script of the esm entry.
export async function main(order: string, held: number[]) {
  const res = await fetch("http://orders.invalid/confirm", { method: "POST", body: JSON.stringify({ order, held }) });
  return await res.json();
}
