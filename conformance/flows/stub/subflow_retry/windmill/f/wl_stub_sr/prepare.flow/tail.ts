// Finishes the preparation; it can fail, which fails the subflow.
export async function main(order: string) {
  const res = await fetch(`http://planning.invalid/finish/${encodeURIComponent(order)}`, { method: "POST" });
  if (!res.ok) throw new Error(`planning: HTTP ${res.status}`);
  return await res.json();
}
