// Returns its argument x and, last, whether x's first element is this
// job's own workspace id, so that a resolved $WM_WORKSPACE shows as a
// stable true while the id itself is masked.
export async function main(x: any) {
  return [...x, x[0] === process.env.WM_WORKSPACE];
}
