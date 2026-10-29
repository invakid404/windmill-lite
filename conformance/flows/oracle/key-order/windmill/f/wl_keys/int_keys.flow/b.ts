// The keys the transform saw, and those of the object received.
export async function main(rk: string[], w: object) {
  return { rk, wk: Object.keys(w) };
}
