// Returns nest(k): [] is 1 level deep and [nest(k-1)] is k levels deep (JE-49).
export async function main(k: number) {
  let v: any[] = [];
  for (let i = 1; i < k; i++) v = [v];
  return v;
}
