// FX-37/a: runs on for 2 s after run-and-wait answered.
export async function main() {
  await new Promise((resolve) => setTimeout(resolve, 2000));
  return 2;
}
