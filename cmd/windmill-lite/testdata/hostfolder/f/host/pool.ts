// A pool the host owns, the second host-provided module of the host-mode
// tests (ADR 0004 host mode item 5): a step takes a lease for its run's
// tenant, and the host releases a run's leases at its onSettled, never
// earlier, because a step that a timeout abandoned may still use them. The
// host registers this instance, so steps and host share it (ADR 0010).

export type Lease = { tenant: string; released: boolean; usedReleased: boolean; lastUse: number; releasedAt: number };

export const leases: Lease[] = [];

export function take(tenant: string): Lease {
  const l = { tenant, released: false, usedReleased: false, lastUse: -1, releasedAt: -1 };
  leases.push(l);
  return l;
}

export function use(l: Lease) {
  l.lastUse = performance.now();
  if (l.released) l.usedReleased = true;
}

/** Releases tenant's leases and returns how many it released. */
export function release(tenant: string): number {
  let n = 0;
  for (const l of leases) {
    if (l.tenant === tenant && !l.released) {
      l.released = true;
      l.releasedAt = performance.now();
      n++;
    }
  }
  return n;
}
