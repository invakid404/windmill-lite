// FX-36/b: a failed flow; run-and-wait answers 422 with its error.
export async function main() {
  throw new Error("z");
}
