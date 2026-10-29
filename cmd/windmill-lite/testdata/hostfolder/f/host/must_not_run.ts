// A step that a test expects never to run.
export function main(from_a?: unknown) {
  throw new Error("must_not_run ran");
}
