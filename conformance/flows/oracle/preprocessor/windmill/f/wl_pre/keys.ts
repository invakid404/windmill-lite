export function preprocessor(event: any) { return { x: { bbb: 1, a: 2, cc: 3 } }; }
export function main(x: any) { return Object.keys(x); }
