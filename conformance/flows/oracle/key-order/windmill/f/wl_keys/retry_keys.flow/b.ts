// Always fails, so b is retried and its skip_if evaluated again.
export async function main() {
  throw new Error("b fails");
}
