// Returns the job's arguments file as the worker wrote it (WR-5), so the
// resolved spelling is visible.
export async function main(v: any) {
  return await Bun.file("args.json").text();
}
