// The same read through a separately bundled copy of f/w1/scopes.ts, as
// Windmill inlines every import into each script's bundle (WR-27): the copy
// has its own AsyncLocalStorage, which no host enters.
import { AsyncLocalStorage } from "node:async_hooks";

const store = new AsyncLocalStorage();

export async function main(args) {
  return { shim: args.shim, copy: store.getStore()?.tenant ?? null };
}
