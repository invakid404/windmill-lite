// A dispatched script that fails (WA-15/a).
export async function main() {
  throw new Error("boom");
}
