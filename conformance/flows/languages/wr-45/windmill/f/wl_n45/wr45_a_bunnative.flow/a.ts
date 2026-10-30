//native
export function main() { return [typeof process.env.WM_TOKEN, process.env.WM_FLOW_STEP_ID, process.env.BASE_URL === process.env.BASE_INTERNAL_URL, typeof process.env.PATH, typeof (process as any).exit]; }
