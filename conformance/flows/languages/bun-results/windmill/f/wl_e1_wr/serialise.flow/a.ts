export async function main() {
  return { a: undefined, f() {}, n: 10n, d: new Date(Date.UTC(2024, 0, 2, 3, 4, 5, 6)), m: new Map([[1, 2]]), u8: new Uint8Array([1, 2]), x: [undefined, NaN, -0] };
}
