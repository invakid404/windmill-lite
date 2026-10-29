// WR-16/g: replace returns a, a raw newline, then a second stream line.
class C {
  replace() {
    return "a\nWM_STREAM: b";
  }
}
export async function* main() {
  yield new C();
}
