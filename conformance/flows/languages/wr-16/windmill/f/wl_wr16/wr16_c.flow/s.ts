// WR-16/c: the chunk's replace is found on its prototype.
class C {
  replace() {
    return "x";
  }
}
export async function* main() {
  yield new C();
}
