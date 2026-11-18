// The resource routes behind getState and setState at one path of type
// state, each answer's status, content type and body, called with fetch at
// BASE_URL and the step's own token, as the stock client calls them: the
// reads getState makes, the exists check and the create or update_value
// that setState makes, and delete. The run deletes its paths first, so a
// repeated run starts from the same state.
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
// Sends a body as bytes with no Content-Type header, which fetch then adds
// none of, and reports the header the request carried (null).
async function bytes(method: string, route: string, body: string): Promise<Answer & { sent_type: string | null }> {
  const req = new Request(`${base}/api/w/${ws}${route}`, { method, headers: auth, body: new TextEncoder().encode(body) });
  const sent_type = req.headers.get("content-type");
  const r = await fetch(req);
  return { sent_type, status: r.status, type: r.headers.get("content-type"), body: scrub(await r.text()) };
}

export async function main() {
  const p = "f/wl_state/a";
  const none = "f/wl_state/none";
  await del(p);
  await del(none);
  const create = (body: string, type: string | null = "application/json") => call("POST", "/resources/create", body, type);
  const update = (path: string, body: string, type: string | null = "application/json") => call("POST", `/resources/update_value/${at(path)}`, body, type);
  const out: Record<string, unknown> = {};
  out.exists_missing = await call("GET", `/resources/exists/${at(p)}`);
  out.get_interpolated_missing = await call("GET", `/resources/get_value_interpolated/${at(p)}`);
  out.get_value_missing = await call("GET", `/resources/get_value/${at(p)}`);
  out.create = await create(JSON.stringify({ path: p, value: { n: 1 }, resource_type: "state" }));
  out.exists = await call("GET", `/resources/exists/${at(p)}`);
  out.create_taken = await create(JSON.stringify({ path: p, value: { n: 9 }, resource_type: "state" }));
  // A path that a resource of another type holds (setup.yaml's).
  out.create_at_other_type = await create(JSON.stringify({ path: "f/wl_state/r", value: { n: 1 }, resource_type: "state" }));
  out.other_type_value_after = await call("GET", `/resources/get_value/${at("f/wl_state/r")}`);
  out.get_interpolated = await call("GET", `/resources/get_value_interpolated/${at(p)}`);
  out.update = await update(p, JSON.stringify({ value: { n: 2 } }));
  out.get_value_updated = await call("GET", `/resources/get_value/${at(p)}`);
  // setState(undefined) on an existing state: JSON.stringify drops the
  // undefined value, so the body is {}.
  out.update_empty = await update(p, "{}");
  out.get_value_after_empty = await call("GET", `/resources/get_value/${at(p)}`);
  out.exists_after_empty = await call("GET", `/resources/exists/${at(p)}`);
  out.update_null = await update(p, JSON.stringify({ value: null }));
  out.get_value_after_null = await call("GET", `/resources/get_value/${at(p)}`);
  out.update_missing = await update(none, JSON.stringify({ value: 1 }));
  out.exists_none = await call("GET", `/resources/exists/${at(none)}`);
  out.delete = await del(p);
  out.delete_missing = await del(p);
  out.exists_deleted = await call("GET", `/resources/exists/${at(p)}`);
  // setState(undefined) on a missing state: the create body has no value.
  out.create_no_value = await create(JSON.stringify({ path: p, resource_type: "state" }));
  out.exists_no_value = await call("GET", `/resources/exists/${at(p)}`);
  out.get_value_no_value = await call("GET", `/resources/get_value/${at(p)}`);
  await del(p);
  // The request's own rejections, before any write.
  out.create_no_type = await create(JSON.stringify({ path: p, value: 1 }));
  out.create_no_path = await create(JSON.stringify({ value: 1, resource_type: "state" }));
  out.create_bad_json = await create("{");
  out.create_no_content_type = await bytes("POST", "/resources/create", JSON.stringify({ path: p, value: 1, resource_type: "state" }));
  out.create_text_plain = await create(JSON.stringify({ path: p, value: 1, resource_type: "state" }), "text/plain");
  out.update_bad_json = await update(p, "{");
  out.update_no_content_type = await bytes("POST", `/resources/update_value/${at(p)}`, JSON.stringify({ value: 1 }));
  out.update_text_plain = await update(p, JSON.stringify({ value: 1 }), "text/plain");
  out.exists_end = await call("GET", `/resources/exists/${at(p)}`);
  await del(p);
  return out;
}
