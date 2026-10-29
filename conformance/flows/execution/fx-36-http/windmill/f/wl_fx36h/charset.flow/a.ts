// FX-36: the content type is written as given, its charset parameter
// included.
export async function main() {
  return { windmill_content_type: "text/plain; charset=iso-8859-1", result: "hi" };
}
