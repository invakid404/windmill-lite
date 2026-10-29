// Returns the iteration's index; throws past 5 so that a loop that
// does not stop fails at once.
export async function main(i: number) {
  if (i > 5) throw new Error("the while loop did not stop");
  return i;
}
