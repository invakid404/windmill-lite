// Step b of the sub-flow: fails when asked to, else returns a value.
export async function main(fail: boolean) {
  if (fail) throw new Error("b failed");
  return "b ok";
}
