// FX-36: base64 transfer encoding decodes the string result.
export async function main() {
  return { windmill_content_type: "text/plain", windmill_content_transfer_encoding: "base64", result: "aGk=" };
}
