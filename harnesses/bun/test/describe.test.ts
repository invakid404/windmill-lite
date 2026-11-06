// Developer checks of describe (runtime/runtime.ts), the text the
// harness's own messages give an error: its stack as Bun builds it. Bun
// 1.3.10 built the stack of an Error that a garbage collection formatted
// before anything read it without the message; Bun 1.4.3 keeps it, which
// the first test checks.

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

test("a stack a collection built before anything read it has its message", () => {
  for (const message of ["x".repeat(5000), "short", "two\nlines"]) {
    for (let i = 0; i < 20; i++) {
      const e = made(message);
      Bun.gc(true);
      const text = describeError(e);
      expect(text.startsWith(`Error: ${message}\n`)).toBe(true);
      expect(text).toContain("    at ");
    }
  }
});

test("a stack is kept as it is", () => {
  const e = new Error("kept");
  void e.stack;
  Bun.gc(true);
  expect(describeError(e)).toBe(e.stack!);
  const bare = new TypeError("boom");
  bare.stack = "TypeError\n    at f (file:///s.ts:1:1)";
  expect(describeError(bare)).toBe("TypeError\n    at f (file:///s.ts:1:1)");
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
