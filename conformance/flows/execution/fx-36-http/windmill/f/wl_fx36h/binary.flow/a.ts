// FX-36: base64 transfer encoding decodes the string result to the bytes
// ff 00 fe, which are not UTF-8.
export async function main() {
  return { windmill_content_type: "application/octet-stream", windmill_content_transfer_encoding: "base64", result: "/wD+" };
}
