// Throws an object whose error shape cannot be serialised (a BigInt).
export function main() {
  throw { message: 1n };
}
