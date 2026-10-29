export async function main(error: any) {
  return { handled: true, saw: error?.message };
}
