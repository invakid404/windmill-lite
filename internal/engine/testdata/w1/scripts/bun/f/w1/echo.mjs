// Returns the arguments it was given, among the names the W1 flows pass
// it: the build binds them by name (WR-6), so it declares each, and leaves
// out those a flow did not pass.
export async function main(x, from_a, from_b, after, greeting, n, name, sum, obj, params) {
  const given = { x, from_a, from_b, after, greeting, n, name, sum, obj, params };
  return Object.fromEntries(Object.entries(given).filter(([, v]) => v !== undefined));
}
