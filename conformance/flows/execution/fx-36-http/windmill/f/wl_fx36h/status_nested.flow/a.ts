// FX-36/c: response metadata with a nested result.
export async function main() {
  return { windmill_status_code: 201, result: { ok: true } };
}
