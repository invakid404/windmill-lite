// A Bun step: the native runtime's process.env holds only the WM_*
// variables and the base URLs (WR-45), so the effect sink's address
// and the outer root reach the native step as its arguments.
export function main() { return { sink: process.env.WL_EFFECT_SINK, root: process.env.WM_ROOT_JOB_ID }; }
