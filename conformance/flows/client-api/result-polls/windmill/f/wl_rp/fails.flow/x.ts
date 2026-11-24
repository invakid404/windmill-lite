// Step x: fails, so that the sub-flow that runs it fails.
export async function main() {
  throw new Error("e");
}
