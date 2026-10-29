// Asks a planning service that only the flow's owners can reach whether
// the order needs its extra step. On the oracle the stub answers instead.
export async function main(order: string) {
  const res = await fetch(`http://planning.invalid/prep/${encodeURIComponent(order)}`);
  if (!res.ok) throw new Error(`planning: HTTP ${res.status}`);
  return await res.json();
}
