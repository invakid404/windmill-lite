// Runs past the timeout of the flow step that runs this flow.
export async function main() {
  await Bun.sleep(5000);
  return 1;
}
