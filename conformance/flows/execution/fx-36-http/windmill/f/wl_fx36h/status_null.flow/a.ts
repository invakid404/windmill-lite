// FX-36/f: every metadata field null: the ordinary response.
export async function main() {
  return { windmill_status_code: null, result: 7 };
}
