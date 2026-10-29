// The keys the transform saw, and those of the object received.
export async function main(ok: string[], o: object) {
  return { ok, received: Object.keys(o) };
}
