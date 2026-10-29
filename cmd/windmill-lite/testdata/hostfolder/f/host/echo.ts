// Returns the arguments it was given, among the names the flows pass it:
// the build binds them by name (WR-6), so it declares each, and leaves out
// those a flow did not pass.
export async function main(x?: unknown, from_a?: unknown, from_b?: unknown, after?: unknown) {
  const given = { x, from_a, from_b, after };
  return Object.fromEntries(Object.entries(given).filter(([, v]) => v !== undefined));
}
