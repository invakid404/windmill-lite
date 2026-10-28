// Developer checks of fitString (runtime/runtime.ts), which keeps a
// warning within max_message however long the error's text is.

import { expect, test } from "bun:test";
import { encode } from "../runtime/frame.ts";
import { fitString } from "../runtime/runtime.ts";

const base = { t: "warning", id: "i1", code: "unattributed_error", message: "" };

test("a message that fits is kept whole", () => {
  expect(fitString(base, "message", "short", 1024)).toEqual({ ...base, message: "short" });
});

test("a cut message is the longest prefix, ending in …, whose payload fits, escapes and multi-byte characters included", () => {
  for (const text of ["x".repeat(5000), '"\\\n'.repeat(900), "é😀".repeat(700), "\u0000".repeat(600)]) {
    for (const limit of [120, 200, 1024]) {
      const m = fitString(base, "message", text, limit)!;
      const msg = m.message as string;
      expect(encode(m).length).toBeLessThanOrEqual(limit);
      expect(msg.endsWith("…")).toBe(true);
      expect(text.startsWith(msg.slice(0, -1))).toBe(true);
      // One more character would not fit, or would split a pair.
      const next = text.slice(0, msg.length);
      if (!/[\uD800-\uDBFF]$/.test(next)) expect(encode({ ...base, message: next + "…" }).length).toBeGreaterThan(limit);
      expect(/[\uD800-\uDBFF]$/.test(msg.slice(0, -1))).toBe(false);
    }
  }
});

test("when not even the envelope fits, there is nothing to send", () => {
  expect(fitString(base, "message", "x".repeat(100), 20)).toBeUndefined();
});
