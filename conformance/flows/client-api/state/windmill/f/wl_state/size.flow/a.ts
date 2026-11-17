// Value size limits: a state value {"s": "<n bytes>"} created at sizes
// doubling from 1 MiB to 32 MiB, each answer kept whole (status, content
// type, body) and each accepted value read back and compared by SHA-256. An
// attempt that gets no HTTP answer fails the run with the error, since it
// says nothing about the size; only an HTTP answer other than 201 counts as
// a refusal. If a size is refused, a bisection between the largest accepted
// and the smallest refused size follows, to 64 KiB. Then update_value of a
// state with the largest accepted value, its answer kept whole and the value
// read back by SHA-256 before the state is deleted. Calls made with fetch at
// BASE_URL and the step's own token. The run returns sizes, answers and
// comparisons, never a value.
const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
const ws = process.env.WM_WORKSPACE!;
const user = process.env.WM_USERNAME ?? "";
const auth = { Authorization: `Bearer ${process.env.WM_TOKEN}` };
type Answer = { status: number; type: string | null; body: string };
// A body that ends in Windmill's Rust source location (a parenthesised
// "(<file>.rs:<line>)" or "@<file>.rs:<line>:<col>" suffixes) is kept
// without it, since windmill-lite gives no such location in its messages.
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
// An HTTP answer, or a thrown error naming the size when there was none.
async function attempt(method: string, route: string, body: string, size: number): Promise<Answer> {
  try {
    return await call(method, route, body);
  } catch (e) {
    throw new Error(`${route} at ${size} bytes got no HTTP answer: ${String((e as Error).message ?? e)}`);
  }
}
const accepted = (a: Answer, status: number) => a.status === status;
const readback = async (p: string, v: string) => {
  const r = await fetch(`${base}/api/w/${ws}/resources/get_value/${at(p)}`, { headers: auth });
  return r.status === 200 && sha(await r.text()) === sha(v);
};
export async function main() {
  const p = "f/wl_state/size";
  const value = (n: number) => JSON.stringify({ s: "x".repeat(n) });
  const tryCreate = async (n: number) => {
    await del(p);
    const v = value(n);
    const answer = await attempt("POST", "/resources/create", `{"path":"${p}","value":${v},"resource_type":"state"}`, n);
    const back = accepted(answer, 201) ? await readback(p, v) : null;
    await del(p);
    return { size: n, answer, readback: back };
  };
  const creates = [];
  let ok = 0, refused = 0;
  for (let n = MiB; n <= 32 * MiB; n *= 2) {
    const t = await tryCreate(n);
    creates.push(t);
    if (accepted(t.answer, 201)) ok = n; else { refused = n; break; }
  }
  if (refused > 0) {
    let lo = ok, hi = refused;
    while (hi - lo > 64 * 1024) {
      const mid = lo + Math.floor((hi - lo) / 2 / 65536) * 65536;
      const t = await tryCreate(mid);
      creates.push(t);
      if (accepted(t.answer, 201)) lo = mid; else hi = mid;
    }
    ok = lo; refused = hi;
  }
  await del(p);
  await call("POST", "/resources/create", JSON.stringify({ path: p, value: 0, resource_type: "state" }));
  const v = value(ok);
  const answer = await attempt("POST", `/resources/update_value/${at(p)}`, `{"value":${v}}`, ok);
  const update = { size: ok, answer, readback: accepted(answer, 200) ? await readback(p, v) : null };
  await del(p);
  return { largest_accepted: ok, smallest_refused: refused, creates, update };
}
