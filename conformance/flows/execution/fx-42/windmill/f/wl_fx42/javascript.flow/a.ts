export async function main(v: any, o: any) {
  return {
    v_keys: Object.keys(v),
    o_keys: Object.keys(o),
    v,
  };
}
