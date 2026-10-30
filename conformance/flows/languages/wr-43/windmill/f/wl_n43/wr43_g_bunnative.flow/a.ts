//native
export function main() { const e = new Error("x"); e.name = "Tagged"; (e as any).message = 7; throw e; }
