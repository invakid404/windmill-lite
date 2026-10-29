// Receives a's whole result.
export async function main(d: { data: string }) {
  return { len: d.data.length, head: d.data.slice(0, 16) };
}
