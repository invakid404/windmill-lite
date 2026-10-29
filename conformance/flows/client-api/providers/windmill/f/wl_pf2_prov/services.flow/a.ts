// The platform services a step reads (milestone PF2, WA-40/b): its
// arguments as the worker resolved them (WR-5, WA-V3/a), and the resource
// and variable reads that the stock client's getResource and getVariable
// make, with fetch at BASE_URL and its own token (WA-10 to WA-12, WA-42).
export async function main(ctx: any, db: any, n: number, plain: string, names: string[]) {
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
  const outer = await get("resources/get_value_interpolated/f/wl_pf2_prov/outer");
  const deep = await get("resources/get_value_interpolated/f/wl_pf2_prov/deep");
  return {
    args: { ctx, db, n, plain, names },
    root_env: e.WM_ROOT_JOB_ID,
    context: await json("resources/get_value_interpolated/f/wl_pf2_prov/context"),
    db: await json("resources/get_value_interpolated/f/wl_pf2_prov/db"),
    raw: await json("resources/get_value/f/wl_pf2_prov/db"),
    exists: [
      (await json("resources/exists/f/wl_pf2_prov/db")).value,
      (await json("resources/exists/f/wl_pf2_prov/none")).value,
      (await json("variables/exists/f/wl_pf2_prov/v")).value,
      (await json("variables/exists/f/wl_pf2_prov/none")).value,
    ],
    variable: await json("variables/get_value/f/wl_pf2_prov/v"),
    secret: await json("variables/get_value/f/wl_pf2_prov/pw"),
    var_prefixed: await get("variables/get_value/$var:f/wl_pf2_prov/v"),
    missing: await get("resources/get_value_interpolated/f/wl_pf2_prov/none"),
    // Windmill ends these two bodies in a Rust location (D14).
    outer: { status: outer.status, type: outer.type, located: outer.text.startsWith("Not found: Resource not found at name f/wl_pf2_prov/missing") },
    deep: { status: deep.status, type: deep.type, internal: deep.text.startsWith("Internal: Maximum resource/variable interpolation depth (50) exceeded; this usually indicates a circular") },
    terminal: await json("resources/get_value_interpolated/f/wl_pf2_prov/terminal"),
  };
}
