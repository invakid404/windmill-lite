// Resource writes beside state on Windmill: create with a type other than
// state (one that exists and one that does not), update_value and delete
// of an entry of another type (one the step created and one setup.yaml
// created), and create with the options the stock client never sends.
// Also the read that the Python client's state probe makes
// (GET resources/get), and the type an entry keeps through update_value,
// which that read shows. Each answer's status, content type and body,
// called with fetch at BASE_URL and the step's own token. The run deletes
// what it creates first, and puts setup.yaml's resource back as it was.
const base = process.env.BASE_INTERNAL_URL ?? process.env.BASE_URL;
const ws = process.env.WM_WORKSPACE!;
const auth = { Authorization: `Bearer ${process.env.WM_TOKEN}` };
type Answer = { status: number; type: string | null; body: string };
async function call(method: string, route: string, body?: string): Promise<Answer> {
  const headers: Record<string, string> = { ...auth };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const r = await fetch(`${base}/api/w/${ws}${route}`, { method, headers, body });
  return { status: r.status, type: r.headers.get("content-type"), body: (await r.text()).replaceAll(ws, "<ws>") };
}
const at = (p: string) => encodeURI(p);
const del = (p: string) => call("DELETE", `/resources/delete/${at(p)}`);
const create = (o: unknown, query = "") => call("POST", `/resources/create${query}`, JSON.stringify(o));
const value = (p: string) => call("GET", `/resources/get_value/${at(p)}`);
// The resource read GET resources/get answers, reduced to its type and
// value (the rest holds timestamps and the caller).
async function typed(p: string) {
  const a = await call("GET", `/resources/get/${at(p)}`);
  if (a.status !== 200) return a;
  const r = JSON.parse(a.body);
  return { status: a.status, type: a.type, resource_type: r.resource_type, value: r.value };
}

export async function main() {
  const obj = "f/wl_writes/obj", unknown = "f/wl_writes/unknown", setup = "f/wl_writes/setup", opt = "f/wl_writes/opt";
  const st = "f/wl_writes/st", none = "f/wl_writes/none";
  for (const p of [obj, unknown, opt, st, none]) await del(p);
  const out: Record<string, unknown> = {};
  out.get_missing = await call("GET", `/resources/get/${at(none)}`);
  await create({ path: st, value: { n: 1 }, resource_type: "state" });
  out.update_state = await call("POST", `/resources/update_value/${at(st)}`, JSON.stringify({ value: { n: 2 } }));
  out.get_state_updated = await typed(st);
  await del(st);
  out.create_other_type = await create({ path: obj, value: { a: 1 }, resource_type: "wl_writes_obj" });
  out.update_other_type = await call("POST", `/resources/update_value/${at(obj)}`, JSON.stringify({ value: { a: 2 } }));
  out.read_other_type = await value(obj);
  out.get_other_type_updated = await typed(obj);
  out.delete_other_type = await del(obj);
  out.create_unknown_type = await create({ path: unknown, value: { a: 1 }, resource_type: "wl_writes_none" });
  out.exists_unknown_type = await call("GET", `/resources/exists/${at(unknown)}`);
  await del(unknown);
  // setup.yaml's resource: updated with the value it has, deleted, and
  // created again as setup.yaml has it.
  out.update_setup = await call("POST", `/resources/update_value/${at(setup)}`, JSON.stringify({ value: { a: 1 } }));
  out.delete_setup = await del(setup);
  out.recreate_setup = await create({ path: setup, value: { a: 1 }, resource_type: "wl_writes_obj" });
  // The options: a state created with each, then update_if_exists over it.
  out.create_description = await create({ path: opt, value: 1, resource_type: "state", description: "d" });
  await del(opt);
  out.create_labels = await create({ path: opt, value: 1, resource_type: "state", labels: ["l"] });
  await del(opt);
  out.create_ws_specific = await create({ path: opt, value: 1, resource_type: "state", ws_specific: true });
  await del(opt);
  out.create_preserve = await create({ path: opt, value: 1, resource_type: "state", preserve_on_behalf_of: true });
  out.create_update_if_exists = await create({ path: opt, value: 2, resource_type: "state" }, "?update_if_exists=true");
  out.read_update_if_exists = await value(opt);
  out.create_update_if_exists_missing = await create({ path: unknown, value: 3, resource_type: "state" }, "?update_if_exists=true");
  for (const p of [obj, unknown, opt, st, none]) await del(p);
  return out;
}
