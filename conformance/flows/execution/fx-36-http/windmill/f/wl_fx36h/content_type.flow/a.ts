// FX-36: with a content type, a JSON string result is the body as text.
export async function main() {
  return { windmill_content_type: "text/plain", result: "hi" };
}
