//native
export function main() { const e = new Error("m"); (e as any).name = 7; throw e; }
