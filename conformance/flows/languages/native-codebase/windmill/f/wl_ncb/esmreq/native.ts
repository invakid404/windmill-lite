//native
export function main() { return [typeof (globalThis as any).Bun, typeof (process as any).version, typeof require]; }
