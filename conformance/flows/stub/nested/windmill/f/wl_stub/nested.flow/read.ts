// Reads a cell from a sheet that only the flow's owners can reach.
export async function main(cell: string) {
  const res = await fetch(`http://sheets.invalid/cells/${encodeURIComponent(cell)}`);
  return await res.text();
}
