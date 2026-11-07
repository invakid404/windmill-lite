// Runs on for 2 s after an early answer.
export async function main() {
  await new Promise((resolve) => setTimeout(resolve, 2000));
  return 2;
}
