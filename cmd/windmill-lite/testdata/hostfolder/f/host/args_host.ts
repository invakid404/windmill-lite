// Reads its own arguments back from args.json through the host-provided
// module argsfile.ts.
import { argsP } from "./argsfile";

export async function main(p: unknown) {
  return { p, file: argsP() };
}
