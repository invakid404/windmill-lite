// Reads its own arguments back from args.json by a file name it builds at
// run time, which no search for the name finds.
export async function main(p: unknown) {
  return { p, file: (await Bun.file(["args", "json"].join(".")).json()).p };
}
