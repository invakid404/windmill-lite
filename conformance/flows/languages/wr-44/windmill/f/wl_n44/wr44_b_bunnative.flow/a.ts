//native
export function main() { const f = (n: number): number => n === 0 ? 0 : 1 + f(n - 1); return f(1e6); }
