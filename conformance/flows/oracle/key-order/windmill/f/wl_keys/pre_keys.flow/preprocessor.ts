// Builds the flow input; o's keys come in the order bbb, a, cc.
export async function preprocessor(event: any) {
  return { o: { bbb: 1, a: 2, cc: 3 } };
}
