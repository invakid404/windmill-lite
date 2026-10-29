// Reports the flow input's keys as the transform saw them, and the
// keys of the whole object it received.
export async function main(fi_keys: string[], fi_whole: object) {
  return { fi_keys, fi_whole_keys: Object.keys(fi_whole) };
}
