// Checks a hold with the same inventory service; it can report that the
// stock moved, which fails the subflow.
export async function main(order: string, held: number) {
  const res = await fetch(`http://inventory.invalid/check/${encodeURIComponent(order)}?held=${held}`);
  if (!res.ok) throw new Error(`inventory: HTTP ${res.status}`);
  return await res.json();
}
