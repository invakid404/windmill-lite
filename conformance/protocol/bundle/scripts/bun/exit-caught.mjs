// Calls process.exit(code), catches the sentinel the harness throws, and
// returns "after": the outcome stays the exit's, and the return is late
// (D11).
export function main(code) {
  try {
    process.exit(code);
  } catch {
    // The call goes on.
  }
  return "after";
}
