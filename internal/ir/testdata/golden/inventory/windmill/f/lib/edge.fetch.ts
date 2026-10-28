export async function main() {
  return (await fetch("https://example.invalid")).status;
}
