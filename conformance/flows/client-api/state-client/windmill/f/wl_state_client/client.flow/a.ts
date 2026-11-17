import * as wmill from "windmill-client@1.862.0";
// What a step observes through the stock client's state helpers: getState
// of a state never written, setState's create then its update, getState
// after each, setState(undefined) on an existing state, the deprecated
// getInternalState and setInternalState at the same state path, an
// explicit path, and setState(undefined) on a missing state, whose create
// carries no value. Each helper's outcome is its value, or the error's
// message, status and body. The run deletes the state first.
const e = process.env;
const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
const del = async (p: string) => { await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text(); };
async function outcome(f: () => Promise<unknown>) {
  try {
    const v = await f();
    return { value: v === undefined ? "<undefined>" : v };
  } catch (err) {
    const x = err as { message?: string; status?: number; body?: unknown };
    return { error: { message: String(x.message).replaceAll(e.WM_WORKSPACE!, "<ws>"), status: x.status ?? null, body: x.body ?? null } };
  }
}
export async function main() {
  const own = e.WM_STATE_PATH!;
  const explicit = "f/wl_state_client/explicit";
  await del(own);
  await del(explicit);
  const out: Record<string, unknown> = { path: own };
  out.get_missing = await outcome(() => wmill.getState());
  out.set_create = await outcome(() => wmill.setState({ n: 1 }));
  out.get_created = await outcome(() => wmill.getState());
  out.set_update = await outcome(() => wmill.setState({ n: 2 }));
  out.get_updated = await outcome(() => wmill.getState());
  out.set_undefined = await outcome(() => wmill.setState(undefined));
  out.get_after_undefined = await outcome(() => wmill.getState());
  out.set_internal = await outcome(() => wmill.setInternalState({ n: 3 }));
  out.get_internal = await outcome(() => wmill.getInternalState());
  out.get_after_internal = await outcome(() => wmill.getState());
  out.explicit_missing = await outcome(() => wmill.getState(explicit));
  out.explicit_set = await outcome(() => wmill.setState({ x: 1 }, explicit));
  out.explicit_get = await outcome(() => wmill.getState(explicit));
  await del(own);
  out.set_undefined_missing = await outcome(() => wmill.setState(undefined));
  out.get_after_undefined_missing = await outcome(() => wmill.getState());
  await del(own);
  await del(explicit);
  return out;
}
