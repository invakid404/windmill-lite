// The same read through a module the contract does not list, which this
// script bundles a copy of: the copy's AsyncLocalStorage reads nothing.
import { store } from "./scopes_copy";

export async function main(shim: unknown) {
  return { shim, copy: store.getStore()?.tenant ?? null };
}
