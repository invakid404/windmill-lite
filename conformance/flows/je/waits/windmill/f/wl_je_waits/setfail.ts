// The first attempt of a retried step (JE-V2): while variable v holds
// "first", set it to "second" and throw. A later attempt sets it back to
// "first", so the next run starts from the same state, and returns x.
export async function main(v: string, x: any) {
  const base = `${process.env.BASE_INTERNAL_URL}/api/w/${process.env.WM_WORKSPACE}/variables`;
  const headers = { Authorization: `Bearer ${process.env.WM_TOKEN}`, "Content-Type": "application/json" };
  const set = async (value: string) => {
    const r = await fetch(`${base}/update/${v}`, { method: "POST", headers, body: JSON.stringify({ value }) });
    if (!r.ok) throw new Error(`variable update failed: ${r.status} ${await r.text()}`);
  };
  const r = await fetch(`${base}/get_value/${v}`, { headers });
  if (!r.ok) throw new Error(`variable read failed: ${r.status} ${await r.text()}`);
  if ((await r.json()) === "first") {
    await set("second");
    throw new Error("first attempt");
  }
  await set("first");
  return x;
}
