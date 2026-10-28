// Text helpers shared by the textual scans of WR-31 and WR-32. Windmill's
// scans are Rust string operations, so lines and whitespace follow Rust's
// rules here: `lines()` splits on "\n" and drops one trailing "\r", and
// `trim()` removes Unicode White_Space, which differs from JavaScript's
// `trim()` (U+0085 is White_Space, U+FEFF is not).

const RUST_WHITE_SPACE = new Set([
  0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0x85, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005,
  0x2006, 0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000,
]);

export function isRustWhiteSpace(c: number): boolean {
  return RUST_WHITE_SPACE.has(c);
}

export function rustTrim(s: string): string {
  let start = 0;
  let end = s.length;
  while (start < end && isRustWhiteSpace(s.charCodeAt(start))) start++;
  while (end > start && isRustWhiteSpace(s.charCodeAt(end - 1))) end--;
  return s.slice(start, end);
}

export function rustLines(s: string): string[] {
  const lines = s.split("\n");
  // The text after the last "\n" is a line only when it is not empty, and
  // keeps a bare "\r"; every other line drops the "\r" of a "\r\n".
  const last = lines.pop()!;
  const out = lines.map((l) => (l.endsWith("\r") ? l.slice(0, -1) : l));
  if (last !== "") out.push(last);
  return out;
}
