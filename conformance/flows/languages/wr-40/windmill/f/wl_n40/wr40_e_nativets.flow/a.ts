export async function main() {
  try { await import("./missing.ts"); return "loaded"; }
  catch (e) { return [e instanceof Error, String((e as Error).message).includes("file:///missing.ts")]; }
}
