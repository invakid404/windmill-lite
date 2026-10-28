// Returns an object whose keys are not in jsonb order (JE-44).
export async function main() {
  return { bbb: 1, a: 2, cc: 3 };
}
