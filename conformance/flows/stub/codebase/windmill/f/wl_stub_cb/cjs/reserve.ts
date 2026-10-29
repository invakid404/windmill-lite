// Holds stock of an item in an inventory that only the flow's owners can
// reach: a codebase script of the entry with no format, CommonJS.
export async function main(item: string, qty: number) {
  const res = await fetch("http://inventory.invalid/hold", { method: "POST", body: JSON.stringify({ item, qty }) });
  return await res.json();
}
