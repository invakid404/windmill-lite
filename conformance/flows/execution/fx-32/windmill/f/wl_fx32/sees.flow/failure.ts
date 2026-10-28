export async function main(x: any, y: any, e: any) {
  return { x, y, e, sid: process.env.WM_FLOW_STEP_ID };
}
