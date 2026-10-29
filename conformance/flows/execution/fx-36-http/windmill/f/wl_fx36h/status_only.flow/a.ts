// FX-36/d: a status code and no result: an empty body.
export async function main() {
  return { windmill_status_code: 200 };
}
