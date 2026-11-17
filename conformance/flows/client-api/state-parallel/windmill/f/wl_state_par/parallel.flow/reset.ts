// Starts every run from one whole value, written by "init.", and no
// readiness marker.

// Helpers, the same in every step of the flow: the API at BASE_URL with
// the step's own token, and the self-validating values the writers write:
// the writer's id repeated to 256 KiB, a bar, then the SHA-256 of that
// text.
const e = process.env;
const auth = { Authorization: `Bearer ${e.WM_TOKEN}` };
const api = `${e.BASE_INTERNAL_URL ?? e.BASE_URL}/api/w/${e.WM_WORKSPACE}/resources`;
const P = "f/wl_state_par/p";
const READY = "f/wl_state_par/ready";
const sha = (s: string) => new Bun.CryptoHasher("sha256").update(s).digest("hex");
function value(id: string): string {
  const body = id.repeat(Math.ceil((256 * 1024) / id.length)).slice(0, 256 * 1024);
  return `${body}|${sha(body)}`;
}
// The writer id of a whole value, or null for a torn or foreign one.
function check(v: unknown): string | null {
  if (typeof v !== "string") return null;
  const bar = v.lastIndexOf("|");
  if (bar < 0) return null;
  const body = v.slice(0, bar);
  if (sha(body) !== v.slice(bar + 1)) return null;
  const id = body.slice(0, body.indexOf("."));
  return value(`${id}.`) === v ? id : null;
}
async function req(method: string, route: string, body?: unknown) {
  const headers: Record<string, string> = { ...auth };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const r = await fetch(`${api}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  return { status: r.status, text: await r.text() };
}
const at = (p: string) => encodeURI(p);
const sleep = (ms: number) => new Promise((res) => setTimeout(res, ms));

export async function main() {
  await req("DELETE", `/delete/${at(P)}`);
  await req("DELETE", `/delete/${at(READY)}`);
  const c = await req("POST", "/create", { path: P, value: value("init."), resource_type: "state" });
  return c.status;
}
