import * as wmill from "windmill-client@1.862.0";
// Two setState calls race for a state never written. The step wraps the
// global fetch that the client calls, and holds each create until both
// exists checks have answered, so both calls see false and both create:
// one create wins, and the other call rejects. The outcomes are sorted, so
// the run does not depend on which call won.
const e = process.env;
const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
async function outcome(f: () => Promise<unknown>) {
  try {
    await f();
    return { ok: true };
  } catch (err) {
    const x = err as { name?: string; message?: string; status?: number; body?: unknown };
    return { error: { name: x.name ?? null, message: String(x.message), status: x.status ?? null, body: x.body ?? null } };
  }
}
export async function main() {
  const p = "f/wl_state_client/race";
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  const real = globalThis.fetch;
  const seen: boolean[] = [];
  let release!: () => void;
  const bothChecked = new Promise<void>((res) => { release = res; });
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/resources/create")) await bothChecked;
    const r = await real(input, init);
    if (url.includes("/resources/exists/")) {
      seen.push((await r.clone().text()) === "true");
      if (seen.length === 2) release();
    }
    return r;
  }) as typeof fetch;
  let both;
  try {
    both = await Promise.all([outcome(() => wmill.setState({ w: "a" }, p)), outcome(() => wmill.setState({ w: "b" }, p))]);
  } finally {
    globalThis.fetch = real;
  }
  const stored = await wmill.getState(p);
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  both.sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
  return { exists_seen: seen, outcomes: both, stored_is_one: stored?.w === "a" || stored?.w === "b" };
}
