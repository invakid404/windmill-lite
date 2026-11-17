// Value size limits: calls made with fetch at BASE_URL and the step's own
// token.
const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
const ws = process.env.WM_WORKSPACE!;
const user = process.env.WM_USERNAME ?? "";
const auth = { Authorization: `Bearer ${process.env.WM_TOKEN}` };
type Answer = { status: number; type: string | null; body: string };
// A body that ends in Windmill's Rust source location (a parenthesised
// "(<file>.rs:<line>)" or "@<file>.rs:<line>:<col>" suffixes) is kept
// without it, since windmill-lite has no such location to give (D14).
const unlocated = (s: string) => s.replace(/ \([a-z_]+\.rs:\d+\)$/, "").replace(/( @[a-z_]+\.rs:\d+:\d+)+$/, "");
const scrub = (s: string) => unlocated(s.replaceAll(ws, "<ws>").replaceAll(`u/${user}/`, "u/<user>/"));
async function call(method: string, route: string, body?: string, type: string | null = "application/json"): Promise<Answer> {
  const headers: Record<string, string> = { ...auth };
  if (body !== undefined && type !== null) headers["Content-Type"] = type;
  const r = await fetch(`${base}/api/w/${ws}${route}`, { method, headers, body });
  return { status: r.status, type: r.headers.get("content-type"), body: scrub(await r.text()) };
}
const at = (p: string) => encodeURI(p);
const del = (p: string) => call("DELETE", `/resources/delete/${at(p)}`);

// The largest state value create and update_value accept: a value
// {"s": "<n bytes>"} at sizes doubling from 1 MiB to 32 MiB, then a
// bisection between the largest accepted and the smallest refused size to
// 64 KiB. The run returns sizes, statuses and texts, never a value; an
// accepted value is read back and compared by SHA-256.
const MiB = 1 << 20;
const sha = (s: string) => new Bun.CryptoHasher("sha256").update(s).digest("hex");
export async function main() {
  const p = "f/wl_state/size";
  const value = (n: number) => JSON.stringify({ s: "x".repeat(n) });
  const tryCreate = async (n: number) => {
    await del(p);
    const v = value(n);
    let a: Answer;
    try {
      a = await call("POST", "/resources/create", `{"path":"${p}","value":${v},"resource_type":"state"}`);
    } catch (e) {
      a = { status: 0, type: null, body: `fetch failed: ${(e as Error).message}` };
    }
    let readback = false;
    if (a.status < 300) {
      const r = await fetch(`${base}/api/w/${ws}/resources/get_value/${at(p)}`, { headers: auth });
      readback = sha(await r.text()) === sha(v);
    }
    await del(p);
    return { ok: a.status < 300, a, readback };
  };
  let ok = 0, refused = 0, refusal: Answer | null = null, readback = true;
  for (let n = MiB; n <= 32 * MiB; n *= 2) {
    const t = await tryCreate(n);
    if (t.ok) { ok = n; readback &&= t.readback; } else { refused = n; refusal = t.a; break; }
  }
  if (refused > 0) {
    let lo = ok, hi = refused;
    while (hi - lo > 64 * 1024) {
      const mid = lo + Math.floor((hi - lo) / 2 / 65536) * 65536;
      const t = await tryCreate(mid);
      if (t.ok) { lo = mid; readback &&= t.readback; } else { hi = mid; refusal = t.a; }
    }
    ok = lo; refused = hi;
  }
  // update_value at the largest accepted size and just above it.
  await del(p);
  await call("POST", "/resources/create", JSON.stringify({ path: p, value: 0, resource_type: "state" }));
  const upd = async (n: number) => {
    const a = await call("POST", `/resources/update_value/${at(p)}`, `{"value":${value(n)}}`);
    return a.status < 300 ? { status: a.status } : a;
  };
  const update_largest = ok > 0 ? await upd(ok) : null;
  const update_above = refused > 0 ? await upd(refused) : null;
  await del(p);
  return { largest_accepted: ok, smallest_refused: refused, refusal, readback, update_largest, update_above };
}
