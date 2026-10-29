// WR-16/a: two string chunks, the first with a real newline.
export async function* main() {
  yield "a\nb";
  yield "c";
}
