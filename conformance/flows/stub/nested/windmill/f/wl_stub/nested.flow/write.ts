// Writes a value to a store that only the flow's owners can reach.
export async function main(row: number, value: string) {
  const res = await fetch("http://store.invalid/rows", { method: "POST", body: JSON.stringify({ row, value }) });
  return await res.json();
}
