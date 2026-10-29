// Posts {root, job, step} to the effect sink and waits for it (OR-16).
export async function main() {
  const res = await fetch(process.env.WL_EFFECT_SINK!, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ root: process.env.WM_ROOT_JOB_ID, job: process.env.WM_JOB_ID, step: "b" }),
  });
  if (!res.ok) throw new Error(`sink: ${res.status}`);
  return "b";
}
