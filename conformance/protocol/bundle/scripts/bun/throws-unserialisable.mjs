// Throws an object whose error shape cannot be serialised: its extra
// holds a cycle (WR-18, WR-19).
export function main() {
  const e = { message: "cyclic" };
  e.self = e;
  throw e;
}
