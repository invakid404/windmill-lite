// Developer checks of describe (runtime/runtime.ts), the text the
// harness's own messages give an error. Bun 1.3.10 builds the stack of an
// Error that a garbage collection formats before anything read it without
// the message; describe puts the message back.

import { expect, test } from "bun:test";
import { describe as describeError } from "../runtime/runtime.ts";

// An error made in a function that has returned, so that a collection
// finds its frame dead and formats the stack.
function made(message: string): Error {
  let e: Error | undefined;
  (() => {
    e = new Error(message);
  })();
  return e!;
}

test("a stack a collection built before anything read it gets its message back", () => {
  for (const message of ["x".repeat(5000), "short", "two\nlines"]) {
    const e = made(message);
    Bun.gc(true);
    const text = describeError(e);
    expect(text.startsWith(`Error: ${message}\n`)).toBe(true);
    expect(text).toContain("    at ");
  }
});

test("a stack whose first line is the name alone gets the message after it", () => {
  const e = new TypeError("boom");
  e.stack = "TypeError\n    at f (file:///s.ts:1:1)";
  expect(describeError(e)).toBe("TypeError: boom\n    at f (file:///s.ts:1:1)");
  const bare = new Error("only");
  bare.stack = "Error";
  expect(describeError(bare)).toBe("Error: only");
});

test("a stack that names its message, or an error without one, is kept as it is", () => {
  const e = new Error("kept");
  void e.stack;
  Bun.gc(true);
  expect(describeError(e)).toBe(e.stack!);
  const empty = new Error();
  empty.stack = "Error\n    at f (file:///s.ts:1:1)";
  expect(describeError(empty)).toBe("Error\n    at f (file:///s.ts:1:1)");
  const named = new RangeError("r");
  named.stack = "RangeError: something else\n    at g";
  expect(describeError(named)).toBe("RangeError: something else\n    at g");
});

test("values that are not Errors, and errors whose reads throw", () => {
  expect(describeError("text")).toBe("text");
  expect(describeError(7)).toBe("7");
  expect(describeError(null)).toBe("null");
  const noStack = new Error("m");
  Object.defineProperty(noStack, "stack", { value: undefined });
  expect(describeError(noStack)).toBe("Error: m");
  const hostile = new Error("m");
  Object.defineProperty(hostile, "stack", {
    get() {
      throw new Error("no");
    },
  });
  expect(describeError(hostile)).toBe("(an error that cannot be printed)");
});
