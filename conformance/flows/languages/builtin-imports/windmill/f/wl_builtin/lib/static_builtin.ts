import { AsyncLocalStorage } from "node:async_hooks";
export async function storage() {
  return new AsyncLocalStorage<number>();
}
