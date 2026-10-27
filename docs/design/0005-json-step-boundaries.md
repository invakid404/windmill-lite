# 0005 · JSON at step boundaries

**Status:** proposed · **Topic:** reproducing Windmill's serialise, store and
hydrate behaviour exactly while keeping the cost to what the semantics
require; the normalising walk, its differential mode, and the benchmark plan.

## Context

In Windmill a value changes form as it moves:

- a Bun step's result is `JSON.stringify(v ?? null)` with an `undefined` →
  `null` replacer and a `BigInt` patch ([WR-13](../semantics/languages.md#wr-13),
  [WR-14](../semantics/languages.md#wr-14)); a native step serialises by its
  own rules, which the reference does not cover yet
  ([0001](0001-equivalence-contract.md) D5);
- that **producer text** is kept in memory as `previous_result` and as
  `stop_after_if`'s `result`, and is also **stored** in PostgreSQL `jsonb`,
  which orders keys (shorter first, then bytewise), keeps the last duplicate,
  rewrites exponent numbers and rejects `\u0000`
  ([FX-42](../semantics/execution.md#fx-42), [FX-48](../semantics/execution.md#fx-48));
- the read **route** decides which form a consumer sees
  ([FX-10](../semantics/execution.md#fx-10), [JE-44](../semantics/expressions.md#je-44),
  [OR-13](../semantics/oracle.md#or-13), [OR-24](../semantics/oracle.md#or-24));
- a step's arguments are stored, then resolved for `$var:`/`$res:` markers
  with insertion order kept, then parsed by the wrapper
  ([WR-10](../semantics/languages.md#wr-10), [WR-V8](../semantics/languages.md#wr-v8),
  [FX-V5](../semantics/execution.md#fx-v5));
- the top-level order of argument maps and of `flow_input` is unspecified
  ([FX-42](../semantics/execution.md#fx-42)).

No single sort reproduces this; the reference withdrew that idea. The
question is where windmill-lite keeps which form, and how cheaply.

## Options considered

1. **Keep live JavaScript values in the harness and copy them with a
   normalising walk** that equals a JSON round trip without building the
   string. This is the cheap path when a segment runs in the harness. With
   the interpreter in Go ([0003](0003-semantics-placement.md)), Go must hold
   the text anyway: the producer text for memory routes, the stored text for
   storage routes, and text to feed QuickJS-ng.
2. **Hold every value in Go as JSON text, in two forms, and never decode what
   is only passed through.** Chosen for the critical path.
3. **Decode into Go maps.** Loses number spelling and key order, and costs
   allocations; rejected.

## Measurements

The walk in Bun 1.4.2 on Apple M4 (prototype outside the repository; the
differential check passed on every fixture and on edge cases: `undefined`,
`-0`, `NaN`, functions and symbols in arrays and objects, `Date`, `Map`,
boxed primitives, `toJSON`, integer-like keys, lone surrogates, shared
references):

| Value | Bytes | Native round trip | Walk | Native + jsonb order | Walk, jsonb mode |
|---|---|---|---|---|---|
| small object | 26 | 0.25 µs | 0.09 µs | 0.42 µs | 0.26 µs |
| message-like object | 3 849 | 5.3 µs | 2.0 µs | 10.3 µs | 7.1 µs |
| 100 messages | 185 451 | 422 µs | 213 µs | 966 µs | 749 µs |
| one 1 MiB string | 1 048 593 | 485 µs | 0.08 µs | 529 µs | 17.6 µs |
| 2 000 small objects | 103 040 | 701 µs | 283 µs | 1 335 µs | 974 µs |

The walk is two to three times faster than a native round trip and shares
strings, which is where the 6 000× on large strings comes from. jsonb
ordering costs about as much again. Across the process boundary, a 3 KB step
round trip costs 18.4 µs and a 100 KB one about 250 µs
([0003](0003-semantics-placement.md)).

## Decision

### Two texts per value, held in Go

The engine's value type (`jsonval`) holds:

- the **producer text**, byte for byte as the harness or QuickJS-ng wrote it;
- the **stored text**, the jsonb-canonical form, computed lazily the first
  time a storage route reads the value;
- a NUL flag, computed eagerly, because every result is stored in Windmill
  and a NUL fails the step there ([OR-27](../semantics/oracle.md#or-27)).

Which form each consumer gets:

| Consumer | Form | Findings |
|---|---|---|
| `previous_result`; `stop_after_if`'s `result`; `results.<preceding id>` in its four exact spellings | producer | FX-42 item 3, JE-8 |
| an engine read of the preceding id when `previous_result` is bound | producer, under the default reading | FX-V6, [0001](0001-equivalence-contract.md) §6 |
| simple `results` paths (the direct lookup), other ids, loop and `branchall` aggregates, preprocessor output, a re-derived `previous_result` when the predecessor succeeded | stored | FX-6, FX-42, JE-9, JE-18 |
| a re-derived `previous_result` when the predecessor did not succeed; the result of an empty chosen `branchone` | the constant two-character JSON string `"{}"` | FX-6, OR-25 |
| `flow_input` values in shortcuts and in the engine | stored, top-level order shuffled per run | JE-7, JE-37, FX-42 item 1, OR-23 |
| `flow_env` values (`later`) | resolved as [JE-38](../semantics/expressions.md#je-38) says, top-level order as stored | JE-7, JE-38 |
| nested argument values a script receives | stored, then resolution | WR-10, WR-V8 |
| the final flow result returned to the host | stored | FX-36, FX-42 item 5 |

### The argument pipeline

1. Each transform yields JSON text: a static value's raw text, a
   shortcut's verbatim text, or QuickJS-ng's `JSON.stringify(x ?? null)`
   output ([JE-15](../semantics/expressions.md#je-15)).
2. The engine assembles the argument object with its top-level keys in an
   order shuffled by the run's seed ([0001](0001-equivalence-contract.md) §2).
3. **Storage boundary:** each argument becomes its stored text. A NUL here
   is a push failure, which is a chaining error routed as
   [FX-15](../semantics/execution.md#fx-15) says
   ([FX-48](../semantics/execution.md#fx-48) case 2): a module failure named
   `Anyhow` at root pickup, a failed child at child pickup, or `InternalError`
   at a later transition. The routing is settled; only the exact message text
   is open ([Q-FX-8](../semantics/execution.md#q-fx-8)) and is pinned by a
   recording.
4. **Resolution:** only arguments whose stored text contains `$var:`,
   `$jsonvar:`, `$res:` or `$encrypted:` are walked, with the depth and
   array rules of [WA-12](../semantics/client-api.md#wa-12) and
   [WA-V3](../semantics/client-api.md#wa-v3). Substituted values enter in
   their source order and are not normalised again; `$jsonvar:` follows the
   default reading of [0001](0001-equivalence-contract.md) §6.
5. The **recorded** arguments (after step 3) and the **resolved** arguments
   (after step 4) are both kept in the run trace: they are distinct
   observation points ([JE-V3](../semantics/expressions.md#je-v3)).
6. The harness parses the text once, with no reviver, as the wrapper does
   ([WR-9](../semantics/languages.md#wr-9)). JavaScript then enumerates
   integer-like keys first, as it would on Windmill.

### The canonicaliser

`jsonval` turns producer text into stored text by streaming over the bytes,
without building a tree of Go values:

- object keys ordered by UTF-8 byte length, then bytewise; the last of
  duplicate keys kept;
- numbers in PostgreSQL `numeric` output form: exponents expanded
  (`1e+21` → `1000000000000000000000`, `1.5e3` → `1500`), `-0` → `0`,
  other digits kept; the scale of an exponent number such as `1.50e3` is
  pinned by an oracle-checked fixture ([Q-FX-7](../semantics/execution.md#q-fx-7));
- strings re-escaped to one canonical escaping; `\u0000` in a string or key
  rejected;
- whitespace removed. jsonb's own text output puts spaces after `:` and `,`;
  no consumer can observe that after parsing, so the compact form is used and
  comparisons are on parsed values.

It is checked three ways: catalogue cases ([FX-42](../semantics/execution.md#fx-42)
a–h, [JE-44](../semantics/expressions.md#je-44), [WR-10](../semantics/languages.md#wr-10)),
an independent Go implementation used only in tests (decode, sort, re-encode),
and, in oracle CI, PostgreSQL itself: the oracle's PostgreSQL container
casts every canonicaliser fixture to `jsonb` and back
([Q-JE-9](../semantics/expressions.md#q-je-9)).

### Results and errors in the harness

The harness serialises each result once with the boundary mode's rules
(Bun: replacer plus `BigInt.prototype.toJSON`; native: plain
`JSON.stringify`) and errors with [WR-18](../semantics/languages.md#wr-18)
(plain `JSON.stringify`, no replacer). The producer text is that string; Go
does not reparse it except where a route or the trace needs a field.

### The normalising walk

The walk is specified and kept, but it is not on the critical path of the
chosen architecture, because every value already crosses a frame as text.
It is used where a JavaScript value would otherwise cross an in-process
boundary without a frame:

1. **Value residency (deferred optimisation).** For a large result whose
   only consumers are later steps in the same harness process, reached
   through verbatim routes, the harness could keep the live value and send Go
   a handle plus the NUL flag and size, and the next invocation would receive
   a walk-made copy (jsonb mode for argument delivery) instead of re-parsing
   the text. The engine would fetch the text only when a route, an
   expression or the trace needs it. This is enabled only if the benchmark
   below shows at least a 2× cut in per-step boundary cost for values of
   64 KiB and more, and the differential mode passes on the whole suite.
2. **Host plugins** that return JavaScript values to be used as module
   results in host mode ([0008](0008-host-hooks.md)) go through the walk in
   the module's boundary mode, so a plugin result is indistinguishable from a
   step result.

The walk equals `JSON.parse(JSON.stringify(v ?? null, replacer))`: `toJSON`
first, then the replacer; non-finite numbers and `-0` as JSON writes them;
functions and symbols dropped in objects and `null` in arrays; boxed
primitives unboxed; own enumerable string keys in their enumeration order; a
cycle throws `TypeError`; every object and array fresh; strings shared. The
jsonb mode additionally orders keys and rejects NUL.

**Differential mode.** With `WL_JSON_DIFFERENTIAL=1`, every walk also runs
the true round trip and the harness fails the invocation, naming the first
differing JSON pointer, if the two differ in value or key order. The engine
does the same for the canonicaliser against the test implementation. CI runs
the whole conformance suite with differential mode on.

### Benchmark plan (milestone G1)

1. Bun: native round trip versus walk, plain and jsonb modes, on the fixture
   shapes above plus generated deep and wide objects.
2. Go: canonicaliser versus decode-sort-encode, and against PostgreSQL in
   oracle CI for correctness only.
3. End to end: the per-step boundary cost (engine → harness → engine) for
   payloads from 26 B to 4 MiB, sequential and with 8 concurrent invocations.
4. Residency prototype versus text transfer, for the gate in item 1 above.

Results are recorded with the runtime versions and hardware; a regression of
more than 20 % against the last recorded run fails the benchmark job, which
runs on demand and nightly, never on every pull request.

## Consequences

- Exactness comes from where the text is produced (the harness and
  QuickJS-ng) and from one canonicaliser, not from re-implementing jsonb in
  every language.
- Go never decodes a value it only passes through.
- Key-order provenance that the oracle has not yet confirmed
  ([Q-WR-4](../semantics/languages.md#q-wr-4), [Q-OR-11](../semantics/oracle.md#q-or-11),
  [Q-OR-12](../semantics/oracle.md#q-or-12)) is encoded as cases; a recording
  that disagrees changes one routing table entry.

## Spec IDs relied on

FX-6, FX-10, FX-36, FX-42, FX-48, FX-V5, FX-V6, CF-20, JE-7 to JE-9, JE-15,
JE-16, JE-18, JE-37, JE-44, JE-V3, WR-9, WR-10, WR-13, WR-14, WR-18, WR-V8,
WA-12, WA-V3, OR-13, OR-23, OR-24, OR-27, Q-FX-7, Q-FX-8, Q-JE-9, Q-WR-4,
Q-OR-11, Q-OR-12.
