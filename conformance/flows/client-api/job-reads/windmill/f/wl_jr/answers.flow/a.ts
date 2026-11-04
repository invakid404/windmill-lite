// Step a of answers: writes one log line, then returns an object whose
// text p reads back from its job's record.
export async function main() {
  console.log("jr-marker");
  return { k: [1, 2.5], s: "x" };
}
