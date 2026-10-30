export function main() { const e = new TypeError("boom"); (e as any).code = 7; throw e; }
