// FX-36/e: a string status code fails the typed decoding.
export async function main() {
  return { windmill_status_code: "201", result: 7 };
}
