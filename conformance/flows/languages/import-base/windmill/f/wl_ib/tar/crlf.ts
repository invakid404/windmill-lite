export function main() {
  const s = main.toString();
  return [s.includes("\r\n"), s.includes("\n")];
}
