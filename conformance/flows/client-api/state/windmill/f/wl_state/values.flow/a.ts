// Values in a state: what jsonb refuses, the stored form a read gives back,
// and the interpolation of $var: and $res: strings on the interpolating
// read. Calls made with fetch at BASE_URL and the step's own token.
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

export async function main() {
  const create = (body: string) => call("POST", "/resources/create", body);
  const read = async (p: string) => ({
    get_value: await call("GET", `/resources/get_value/${at(p)}`),
    get_interpolated: await call("GET", `/resources/get_value_interpolated/${at(p)}`),
  });
  const paths = ["f/wl_state/nul", "f/wl_state/stored", "f/wl_state/vars", "f/wl_state/whole", "f/wl_state/missing_var"];
  for (const p of paths) await del(p);
  const out: Record<string, unknown> = {};
  // Values jsonb refuses: a NUL character, and a lone surrogate.
  out.create_nul = await create('{"path":"f/wl_state/nul","value":{"s":"a\\u0000b"},"resource_type":"state"}');
  out.exists_nul = await call("GET", `/resources/exists/${at("f/wl_state/nul")}`);
  out.create_surrogate = await create('{"path":"f/wl_state/nul","value":{"s":"\\ud800"},"resource_type":"state"}');
  out.exists_surrogate = await call("GET", `/resources/exists/${at("f/wl_state/nul")}`);
  await del("f/wl_state/nul");
  await create(JSON.stringify({ path: "f/wl_state/nul", value: { s: "ok" }, resource_type: "state" }));
  out.update_nul = await call("POST", `/resources/update_value/${at("f/wl_state/nul")}`, '{"value":{"s":"a\\u0000b"}}');
  out.after_update_nul = await read("f/wl_state/nul");
  // The stored form: keys out of jsonb's order, numbers spelled as a
  // client would not write them, an escaped character; written by create
  // and again by update_value.
  const odd = '{"b":1,"a":1.0,"ab":1e3,"c":{"y":[1.50,"\\u0041",-0,1E-2],"x":null},"":true}';
  out.create_stored = await create(`{"path":"f/wl_state/stored","value":${odd},"resource_type":"state"}`);
  out.stored = await read("f/wl_state/stored");
  out.update_stored = await call("POST", `/resources/update_value/${at("f/wl_state/stored")}`, `{"value":${odd}}`);
  out.stored_updated = await read("f/wl_state/stored");
  // Interpolation on read: $var: and $res: strings in a state value, at the
  // top and nested, and a whole value that names a missing variable.
  await create(JSON.stringify({
    path: "f/wl_state/vars",
    value: { v: "$var:f/wl_state/v", r: "$res:f/wl_state/r", o: { v: "$var:f/wl_state/v", l: ["$var:f/wl_state/v"] }, plain: "x $var:f/wl_state/v" },
    resource_type: "state",
  }));
  out.vars = await read("f/wl_state/vars");
  await create(JSON.stringify({ path: "f/wl_state/whole", value: "$var:f/wl_state/v", resource_type: "state" }));
  out.whole = await read("f/wl_state/whole");
  await create(JSON.stringify({ path: "f/wl_state/missing_var", value: { v: "$var:f/wl_state/none" }, resource_type: "state" }));
  out.missing_var = await read("f/wl_state/missing_var");
  for (const p of paths) await del(p);
  return out;
}
