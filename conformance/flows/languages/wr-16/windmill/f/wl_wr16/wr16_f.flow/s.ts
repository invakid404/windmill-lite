// WR-16/f: replace returns an object whose valueOf gives 7 and whose
// toString gives x; string + decides which the stream holds.
class C {
  replace() {
    return { valueOf: () => 7, toString: () => "x" };
  }
}
export async function* main() {
  yield new C();
}
