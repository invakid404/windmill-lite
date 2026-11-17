// Which paths a create of type state accepts: prefixes, a folder that does
// not exist, user paths, nesting and characters. Each created entry is
// read back and deleted. Calls made with fetch at BASE_URL and the step's
// own token.
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

export async function main() {
  const tries: Record<string, string> = {
    bad_prefix: "x/wl_state/bad",
    no_prefix: "wl_state_bad",
    missing_folder: "f/wl_state_nofolder/x",
    folder_only: "f/wl_state",
    own_user: `u/${user}/wl_state_own`,
    other_user: "u/wl_state_nobody/x",
    nested: "f/wl_state/deep/er/path",
    dash_underscore: "f/wl_state/a-b_C9",
    dot: "f/wl_state/a.b",
    space: "f/wl_state/a b",
    unicode: "f/wl_state/ü",
    group: "g/wl_state_group/x",
    trailing_slash: "f/wl_state/slash/",
  };
  const out: Record<string, unknown> = {};
  for (const [k, p] of Object.entries(tries)) {
    await del(p);
    const created = await call("POST", "/resources/create", JSON.stringify({ path: p, value: { k }, resource_type: "state" }));
    const exists = await call("GET", `/resources/exists/${at(p)}`);
    const read = await call("GET", `/resources/get_value_interpolated/${at(p)}`);
    const deleted = await del(p);
    out[k] = { created, exists, read, deleted };
  }
  return out;
}
