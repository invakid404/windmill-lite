// FX-12/a: an Error with an own property; over HTTP, 422.
export async function main() {
  const e: any = new Error("boom");
  e.code = 7;
  throw e;
}
