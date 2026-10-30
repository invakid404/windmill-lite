export function main() { const e = new Error("m"); (e as any).stack = 5; throw e; }
