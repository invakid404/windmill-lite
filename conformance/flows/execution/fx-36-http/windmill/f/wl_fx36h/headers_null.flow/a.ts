// FX-36/g: a non-null metadata field and a null result: 204.
export async function main() {
  return { windmill_headers: {}, result: null };
}
