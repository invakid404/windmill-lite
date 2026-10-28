export async function main() {
  throw Object.assign(new Error("boom"), { code: 7 });
}
