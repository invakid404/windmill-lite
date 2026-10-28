# 0005 · JSON at step boundaries

**Status:** proposed · **Topic:** reproducing Windmill's serialise, store and
hydrate behaviour exactly while keeping the cost to what the semantics
require; why the normalising walk was dropped; and the benchmark plan.

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
   string. It would be the cheap path if steps were chained inside the
   harness. With
   the interpreter in Go ([0003](0003-semantics-placement.md)), Go must hold
   the text anyway: the producer text for memory routes, the stored text for
   storage routes, and text to feed QuickJS-ng.
2. **Hold every value in Go as JSON text, in two forms, and never decode what
   is only passed through.** Chosen.
3. **Decode into Go maps.** Loses number spelling and key order, and costs
   allocations; rejected.

## Measurements

A prototype walk in Bun 1.4.2 on Apple M4 (outside the repository) was two
to three times faster than a native round trip (`JSON.parse(JSON.stringify(v))`)
on objects of 26 B to 185 KB (for example 2.0 µs against 5.3 µs for a 3.8 KB
object), and shared strings (0.08 µs against 485 µs for a 1 MiB string);
jsonb key ordering roughly doubled either cost. It was also **not
equivalent**. On Bun 1.3.10 and 1.4.2 alike:

| Input | Native round trip | Walk |
|---|---|---|
| a top-level function | throws `SyntaxError` (no text to parse) | `null` |
| a top-level symbol | throws `SyntaxError` | `null` |
| a parsed object with an own `__proto__` key | keeps the key | loses it |

and a differential mode that ran both evaluated getters and `toJSON` twice,
so a stateful `toJSON` changed the value between the two runs. Across the
process boundary, a 3 KB step round trip costs 18.4 µs and a 100 KB one about
250 µs ([0003](0003-semantics-placement.md)).

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
   at a later transition. That routing is source-derived at medium
   confidence; the exact message text
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
  a number outside `numeric`'s range rejected, as PostgreSQL rejects it;
- strings re-escaped to `jsonb`'s own output escaping; `\u0000` in a string
  or key rejected, and so is a surrogate escape without its pair, as
  PostgreSQL rejects both;
- whitespace removed. jsonb's own text output puts spaces after `:` and `,`;
  no consumer can observe that after parsing, so the compact form is used and
  comparisons are on parsed values. The expression shortcuts
  ([JE-6](../semantics/expressions.md#je-6) to
  [JE-9](../semantics/expressions.md#je-9)) answer texts unparsed, and a
  site can quote their answer (Consequences).

It is checked three ways: catalogue cases ([FX-42](../semantics/execution.md#fx-42)
a–h, [JE-44](../semantics/expressions.md#je-44), [WR-10](../semantics/languages.md#wr-10)),
an independent Go implementation used only in tests (decode, sort, re-encode),
and PostgreSQL itself: a CI job casts every canonicaliser fixture to `jsonb`
and back in the oracle's PostgreSQL image, pinned by the same digest
([Q-JE-9](../semantics/expressions.md#q-je-9)).

### Results and errors in the harness

The harness serialises each result once with the boundary mode's rules
(Bun: replacer plus `BigInt.prototype.toJSON`; native: the rules the
semantics request will settle, D5) and errors with
[WR-18](../semantics/languages.md#wr-18) (plain `JSON.stringify`, no
replacer). The producer text is that string; Go does not reparse it except
where a route or the trace needs a field. A value that the serialiser
cannot turn into text (a top-level function or symbol) fails the step as an
`error` outcome with the WR-18 shape and name `TypeError`, which is what the
reference expects from the wrapper's result write
([WR-13](../semantics/languages.md#wr-13) case d). The message is pinned when
[Q-WR-12](../semantics/languages.md#q-wr-12) is recorded; until then that
case compares the name only.

**Host-plugin results** ([0008](0008-host-hooks.md)) that stand in for a
module's result are serialised the same way, with the module's boundary mode,
by the same function. A plugin result is therefore indistinguishable from a
step result by construction, not by a second implementation.

### No normalising walk in the first edition

The walk is dropped: every value crosses a frame as text anyway, the walk's
only remaining uses were host-plugin results (above) and a speculative
"value residency" optimisation, and it was not equivalent to the round trip
it was meant to replace. Keeping it would also have meant a second jsonb
canonicaliser in TypeScript beside `jsonval`, against the single-source rule.
If benchmark G1 shows that large values crossing the boundary dominate a
real workload, residency gets its own decision record, with a shared fixture
set run by both canonicalisers and an equivalence contract written for the
walk's result and errors.

### Benchmark plan (milestone G1)

1. Go: the canonicaliser versus decode, sort and encode, and against
   PostgreSQL in oracle CI for correctness only.
2. End to end: the per-step boundary cost (engine → harness → engine) for
   payloads from 26 B to 4 MiB, sequential and with 8 concurrent
   invocations, split into transport, harness parse and serialise, and Go
   handling.
3. The share of a run's time spent at boundaries, from the run spans of
   [0011](0011-differential-testing.md), on the conformance flows and on any
   host workload a host provides.

Results are recorded with the runtime versions and hardware; a regression of
more than 20 % against the last recorded run fails the benchmark job, which
runs on demand and nightly, never on every pull request.

## Consequences

- Exactness comes from where the text is produced (the harness and
  QuickJS-ng) and from one canonicaliser, not from re-implementing jsonb in
  every language, and not from a JavaScript copy that only approximates a
  round trip.
- Go never decodes a value it only passes through.
- Key-order provenance that the oracle has not yet confirmed
  ([Q-WR-4](../semantics/languages.md#q-wr-4), [Q-OR-11](../semantics/oracle.md#q-or-11),
  [Q-OR-12](../semantics/oracle.md#q-or-12)) is encoded as cases; a recording
  that disagrees changes one routing table entry.
- Milestone A1 changed two points. The PostgreSQL check runs in the ordinary
  CI workflow rather than only in oracle CI, because it needs PostgreSQL but
  no Windmill. And the canonicaliser rejects two texts besides NUL that
  PostgreSQL's `jsonb` input rejects, an unpaired surrogate escape and a
  number outside `numeric`'s range. The reference covers only NUL, so
  whether Windmill fails a step on them as it does on NUL is request 4 of the
  [README](README.md); until then the eager flag covers NUL alone.
- Milestone B2a found that the spaces of jsonb's text output can show.
  The [JE-9](../semantics/expressions.md#je-9) shortcut answers the
  database's text unparsed, and the `timeout` site's parse error quotes it:
  recording batch E3a recorded `Value was: {"a": 2, "b": 1}` for a
  `results.p` read through the database, against `Value was: {"b":1,"a":2}`
  for the same read of the previous step from memory
  (`conformance/flows/je/shortcuts`, runs `je8-order-timeout-db` and
  `je8-order-timeout`). The expression pipeline therefore applies the path
  to the stored text and answers in jsonb's text output, a space after each
  `:` and `,` outside strings; the stored text stays compact. The other
  shortcuts answer the texts they are given as they are: JE-6 and JE-8
  `previous_result`, JE-7 a member of the flow input. Some of those values
  are read from the database too
  ([JE-44](../semantics/expressions.md#je-44)): the flow input's members,
  and `previous_result` after a loop or `branchall` or a re-fetch. Whether
  Windmill's text there is jsonb's spaced output is not recorded (request 8
  of the [README](README.md)); until it is, the routing table's stored text
  stands.

## Spec IDs relied on

FX-6, FX-10, FX-36, FX-42, FX-48, FX-V5, FX-V6, CF-20, JE-7 to JE-9, JE-15,
JE-16, JE-18, JE-37, JE-44, JE-V3, WR-9, WR-10, WR-13, WR-14, WR-18, WR-V8, Q-WR-12,
WA-12, WA-V3, OR-13, OR-23, OR-24, OR-27, Q-FX-7, Q-FX-8, Q-JE-9, Q-WR-4,
Q-OR-11, Q-OR-12.
