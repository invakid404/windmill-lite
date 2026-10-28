// Blocks the event loop for args.ms, then returns args.ms.
export function main(args) {
  const end = Date.now() + args.ms;
  while (Date.now() < end) {}
  return args.ms;
}
