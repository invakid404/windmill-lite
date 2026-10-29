// Index 0 is slow, so the other bodies complete first.
export async function main() {
  await new Promise((resolve) => setTimeout(resolve, 1500));
  return 0;
}
