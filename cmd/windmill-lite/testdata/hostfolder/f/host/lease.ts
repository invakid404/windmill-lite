// Takes a lease of the host's pool for its run's tenant and uses it every
// 10 ms for ms, while its timeout of 1 s abandons it: the lease must stay
// unreleased as long as the step runs (ADR 0004 host mode item 5).
import { take, use } from "./pool";
import { store } from "./scopes";

export async function main(ms: number) {
  const l = take(store.getStore()?.tenant ?? "");
  const started = Date.now();
  while (Date.now() - started < ms) {
    use(l);
    await Bun.sleep(10);
  }
  use(l);
  return l.usedReleased;
}
