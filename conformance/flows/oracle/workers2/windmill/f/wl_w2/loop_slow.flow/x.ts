// Iteration 0 is slow, so iteration 1 completes first.
export async function main(v: number) {
  if (v === 0) await new Promise((resolve) => setTimeout(resolve, 1500));
  return v;
}
