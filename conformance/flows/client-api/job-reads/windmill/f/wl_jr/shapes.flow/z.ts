// The second step of loop p's body: ten times the first step's result. The
// second iteration waits half a second first, so that it completes last on
// any number of workers: the loop's status names the child that completed
// last (CF-22).
export async function main(v: number) {
  if (v === 2) await Bun.sleep(500);
  return v * 10;
}
