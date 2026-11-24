// A result larger than the job read's record shows: 120,000 characters
// from a fixed pseudo-random sequence, which do not compress, so that the
// record replaces them.
export async function main() {
  const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let x = 2463534242;
  let s = "";
  for (let i = 0; i < 120000; i++) {
    x = (x ^ (x << 13)) >>> 0;
    x = (x ^ (x >>> 17)) >>> 0;
    x = (x ^ (x << 5)) >>> 0;
    s += abc[x % abc.length];
  }
  return s;
}
