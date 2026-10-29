// The extra step, which the planning service can skip.
export async function main(order: string) {
  const res = await fetch(`http://planning.invalid/extra/${encodeURIComponent(order)}`, { method: "POST" });
  if (!res.ok) throw new Error(`planning: HTTP ${res.status}`);
  return await res.json();
}
