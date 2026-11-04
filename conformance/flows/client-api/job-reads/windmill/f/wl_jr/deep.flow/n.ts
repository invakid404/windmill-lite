// Step n: the run's root job id, a nonce that keys the sub-flow's cache
// entry, so each run starts with an empty cache.
export async function main() {
  return process.env.WM_ROOT_JOB_ID;
}
