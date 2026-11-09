// Returns its arguments as the worker resolved them, and the interpolated
// and raw reads of resources whose whole value is one reference, made with
// fetch at BASE_URL and its own token, as the stock client's getResource.
export async function main(sv: any, ss: any, sj: any, sa: any, sm: any, se: any, ev: any, em: any) {
  const e = process.env;
  const get = async (path: string) => {
    const res = await fetch(`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/${path}`, {
      headers: { Authorization: `Bearer ${e.WM_TOKEN}` },
    });
    const type = (res.headers.get("content-type") ?? "").split(";")[0];
    return { status: res.status, type, text: await res.text() };
  };
  const json = async (path: string) => {
    const r = await get(path);
    return { status: r.status, type: r.type, value: JSON.parse(r.text) };
  };
  const interp = (r: string) => json(`resources/get_value_interpolated/f/wl_wv/${r}`);
  const missing = await get("resources/get_value_interpolated/f/wl_wv/rx");
  return {
    args: { sv, ss, sj, sa, sm, se, ev, em },
    interp: { rv: await interp("rv"), rj: await interp("rj"), rm: await interp("rm"), re: await interp("re") },
    raw: { rv: await json("resources/get_value/f/wl_wv/rv"), rx: await json("resources/get_value/f/wl_wv/rx") },
    // Windmill may end this body in a Rust location (D14).
    missing: { status: missing.status, type: missing.type, starts: missing.text.startsWith("Not found: Variable f/wl_wv/missing not found") },
  };
}
