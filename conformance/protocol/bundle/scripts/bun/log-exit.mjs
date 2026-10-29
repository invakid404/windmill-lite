// Writes lines log lines of about 100 characters each, then calls
// process.exit(code).
export function main(lines, code) {
  for (let i = 0; i < lines; i++) console.log(`line ${i} ${"x".repeat(90)}`);
  process.exit(code);
}
