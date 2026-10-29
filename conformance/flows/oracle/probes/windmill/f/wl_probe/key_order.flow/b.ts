// The keys of the previous result and of results.a, read in the
// transforms and from the received objects.
export async function main(p_keys: string[], r_keys: string[], p_whole: object, r_whole: object) {
  return { p_keys, r_keys, p_whole_keys: Object.keys(p_whole), r_whole_keys: Object.keys(r_whole) };
}
