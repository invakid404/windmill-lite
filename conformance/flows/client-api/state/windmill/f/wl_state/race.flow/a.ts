// The create race: calls made with fetch at BASE_URL and the step's own
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

// Two first writes race: both creates are in flight together, as when two
// setState calls both saw exists answer false.
export async function main() {
  const p = "f/wl_state/race";
  await del(p);
  const body = (w: string) => JSON.stringify({ path: p, value: { w }, resource_type: "state" });
  const [a, b] = await Promise.all([
    call("POST", "/resources/create", body("a")),
    call("POST", "/resources/create", body("b")),
  ]);
  const won = a.status < 300 ? a : b;
  const lost = a.status < 300 ? b : a;
  const winner = a.status < 300 ? "a" : "b";
  const read = await call("GET", `/resources/get_value/${at(p)}`);
  await del(p);
  return {
    statuses: [a.status, b.status].sort(),
    won,
    lost,
    stored_is_winner: read.status === 200 && JSON.parse(read.body).w === winner,
  };
}
