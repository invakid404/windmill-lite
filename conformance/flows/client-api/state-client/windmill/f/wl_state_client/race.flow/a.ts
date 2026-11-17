import * as wmill from "windmill-client@1.862.0";
// Two setState calls race for a state never written: both are in flight
// together, so both can see exists answer false and both create. The
// outcomes are sorted, so the run does not depend on which call won.
const e = process.env;
const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
async function outcome(f: () => Promise<unknown>) {
  try {
    await f();
    return { ok: true };
  } catch (err) {
    const x = err as { message?: string; status?: number; body?: unknown };
    return { error: { message: String(x.message), status: x.status ?? null, body: x.body ?? null } };
  }
}
export async function main() {
  const p = "f/wl_state_client/race";
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  const both = await Promise.all([outcome(() => wmill.setState({ w: "a" }, p)), outcome(() => wmill.setState({ w: "b" }, p))]);
  const stored = await wmill.getState(p);
  await (await fetch(`${api}/delete/${encodeURI(p)}`, { method: "DELETE", headers: auth })).text();
  both.sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
  return { outcomes: both, stored_is_one: stored?.w === "a" || stored?.w === "b" };
}
