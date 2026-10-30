export async function main() {
  await new Promise((r) => setTimeout(r, 2000));
  return { stage: "b" };
}
