// FX-36/a: an object result; run-and-wait answers 200 with it.
export async function main() {
  return { b: 1, a: 2 };
}
