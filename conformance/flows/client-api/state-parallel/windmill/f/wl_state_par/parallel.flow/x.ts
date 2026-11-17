// One iteration of the parallel loop, on its own worker: the reader marks
// itself ready and reads the state for 8 s from four concurrent loops,
// checking that every read is one whole value a writer wrote; the writer
// waits for the reader, then writes from four concurrent loops for 6 s,
// each loop its own value w0. to w3., and ends with one more write of
// "final." that it reads back.

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

export async function main(role: string) {
  if (role === "r") {
    await req("POST", "/create", { path: READY, value: true, resource_type: "state" });
    const until = Date.now() + 8000;
    let reads = 0, torn = 0, failed = 0;
    const seen = new Set<string>();
    await Promise.all([0, 1, 2, 3].map(async () => {
      while (Date.now() < until) {
        const r = await req("GET", `/get_value_interpolated/${at(P)}`);
        reads++;
        if (r.status !== 200) { failed++; continue; }
        const id = check(JSON.parse(r.text));
        if (id === null) torn++; else seen.add(id);
      }
    }));
    return { role, torn, failed, every_read_whole: reads > 0 && torn === 0 && failed === 0, saw_writers: [...seen].filter((s) => s.startsWith("w")).length >= 2 };
  }
  for (let i = 0; i < 600; i++) {
    if ((await req("GET", `/exists/${at(READY)}`)).text === "true") break;
    await sleep(100);
  }
  const until = Date.now() + 6000;
  let failed = 0;
  await Promise.all([0, 1, 2, 3].map(async (w) => {
    const v = value(`w${w}.`);
    while (Date.now() < until) {
      const u = await req("POST", `/update_value/${at(P)}`, { value: v });
      if (u.status !== 200) failed++;
    }
  }));
  const f = await req("POST", `/update_value/${at(P)}`, { value: value("final.") });
  const back = await req("GET", `/get_value_interpolated/${at(P)}`);
  return { role, failed, final: f.status, final_read_back: check(JSON.parse(back.text)) === "final" };
}
