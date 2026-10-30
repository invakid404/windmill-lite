export async function storage() {
  const { AsyncLocalStorage } = await import("node:async_hooks");
  return new AsyncLocalStorage<number>();
}
