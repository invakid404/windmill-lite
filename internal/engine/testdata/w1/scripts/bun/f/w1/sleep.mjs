// Resolves with args.ms after waiting that long on a timer.
export async function main(args) {
  await Bun.sleep(args.ms);
  return args.ms;
}
