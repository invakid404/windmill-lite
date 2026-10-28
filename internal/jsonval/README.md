# internal/jsonval

**Current state:** implemented: the value type of milestone K1 and the jsonb
canonicaliser of milestone A1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)). This README
covers `internal/jsonval`. Other packages use the type in their contracts, but
nothing computes stored text yet: the engine, which applies the routing
table, does not exist.

## The value type

[ADR 0005](../../docs/design/0005-json-step-boundaries.md) holds every value
as JSON text and never decodes what it only passes through. A `Value` has
three parts:

| Part | When it is computed | Accessor |
|---|---|---|
| the producer text, byte for byte as a harness, QuickJS-ng, a static transform or the host wrote it | when the value is made (`FromProducer`) | `Producer` |
| the NUL flag: the text holds the escape `\u0000`, which PostgreSQL rejects ([OR-27](../../docs/semantics/oracle.md#or-27), [FX-48](../../docs/semantics/execution.md#fx-48)) | when the value is made | `HasNUL` |
| the stored text, the text PostgreSQL `jsonb` gives back for the producer text ([FX-42](../../docs/semantics/execution.md#fx-42), [JE-44](../../docs/semantics/expressions.md#je-44)) | on first use, once | `Stored` |

- **Ownership.** `FromProducer` takes ownership of its byte slice without
  copying or validating it. Callers must not modify what `Producer` or
  `Stored` return. `Stored` shares the producer's bytes when they are already
  in stored form.
- **Absent values.** The zero `Value` means no value at all. JSON `null` is a
  value.
- **JSON encoding.** `MarshalJSON` returns the producer text and refuses the
  zero `Value`; tag optional fields `json:",omitzero"`. `encoding/json` then
  compacts that text and escapes `<`, `>` and `&` (unless
  `SetEscapeHTML(false)`), which keeps the parsed value but not the bytes. A
  writer that must keep the producer's bytes, such as a harness frame or a
  recording, writes `Producer()` itself. `UnmarshalJSON` keeps a copy of the
  raw text, so a value read from a frame keeps its spelling.
- **Concurrency.** A `Value` is immutable and safe for concurrent use.

The engine applies ADR 0005's routing table, which decides whether each
consumer sees the producer or the stored text. This package only holds the
two texts.

## The stored text

The canonicaliser makes one pass over the producer text and builds no Go
values. Its output is `jsonb`'s text output without the spaces `jsonb` puts
after `:` and `,`, which no consumer can see after parsing:

| Part of the text | Stored form |
|---|---|
| object keys | ordered by the decoded key's UTF-8 byte length, then bytewise; of duplicate keys only the last is kept |
| numbers | PostgreSQL `numeric` output: plain decimal with the exponent applied (`1e+21` → `1000000000000000000000`), `max(0, fraction digits − exponent)` digits after the point (`1.50e-3` → `0.00150`, `1.50e3` → `1500`), other digits kept (`1.50`), and no sign on zero (`-0.0` → `0.0`) |
| strings | escapes decoded, then written as `jsonb` writes them: `\"`, `\\`, `\b`, `\f`, `\n`, `\r`, `\t`, other control characters as `\u00xx` with lowercase digits, and everything else, `/` and U+2028 included, as raw UTF-8 |
| whitespace | none |

Stored text is a fixed point: a `Value` made from stored text has that text as
its stored text.

**Cost.** One pass, plus a copy of each object whose keys are not already in
order. An enclosing object that is reordered copies the inner bytes again, so
the worst case is the text's size times the number of reordered objects on
one path; Windmill fails context values nested deeper than 127 levels
([JE-49](../../docs/semantics/expressions.md#je-49)). The canonicaliser keeps
its own stack, so deep nesting cannot overflow Go's.

## Rejections

`Stored` fails with a `*StoreError` for the first token, in text order, that
`jsonb` input rejects, as PostgreSQL reports the first one it meets. Its
`Kind` is one of four sentinels, so `errors.Is` works on it, its `Offset` is
the token's byte offset, and `DatabaseMessage` gives PostgreSQL's message:
the text that follows `error returned from database: ` in the error Windmill
reports when storing fails. The engine routes these failures as FX-48 says.

| Kind | The text holds | `DatabaseMessage` | Source |
|---|---|---|---|
| `ErrNUL` | the escape `\u0000` | `unsupported Unicode escape sequence` | [FX-48](../../docs/semantics/execution.md#fx-48); the message is recorded in [OR-27](../../docs/semantics/oracle.md#or-27) |
| `ErrUnpairedSurrogate` | a surrogate escape that is not half of a high-low pair | `invalid input syntax for type json` | PostgreSQL; not in the reference |
| `ErrNumericOverflow` | a number with more than 131072 digits before the point or 16383 after it, or an exponent beyond 2³⁰−1 in magnitude | `value overflows numeric format` | PostgreSQL; not in the reference |
| `ErrSyntax` | anything that is not one JSON value in valid UTF-8 | `invalid input syntax for type json` | producers never write it |

`HasNUL` is the one rejection known without canonicalising, as ADR 0005
specifies. The reference states only the NUL case. Whether Windmill fails a
step whose result holds an unpaired surrogate or an out-of-range number, as
PostgreSQL's rejection implies, is request 4 to the semantics team
([docs/design](../../docs/design/README.md)).

Not reproduced: PostgreSQL's nesting limit, which depends on the server's
stack size; `jsonb`'s limits on a string's length (256 MiB) and on a value's
size; and PostgreSQL's own message for invalid UTF-8, which never reaches a
cast because PostgreSQL refuses such text earlier.

## Tests

ADR 0005 checks the canonicaliser three ways, and all three run in CI:

1. **The conformance cases** in [conformance/jsonb](../../conformance/README.md),
   which cite their catalogue cases and findings
   (`TestConformanceStored`). `TestConformanceStoredCatalogue` fails unless
   every catalogue case of each finding there is a case or is deferred with a
   reason.
2. **The test implementation**, `referenceStored` in `reference_test.go`: an
   independent decode, sort and re-encode built on `encoding/json`, regular
   expressions and `math/big`, with no code shared with the canonicaliser.
   Edge cases, seeded generated text, the conformance cases and the fuzz
   target `FuzzStored` compare the two.
3. **PostgreSQL itself.** `TestStoredMatchesPostgreSQL` casts every input of
   the other tests to `jsonb` and back and compares. It runs only when
   `WL_JSONB_PSQL` holds a `psql` command line (split on spaces) that reaches
   a server. CI's `jsonb (PostgreSQL)` job sets it to `psql` inside the
   oracle's PostgreSQL image, pinned by the digest in
   [oracle.md](../../docs/semantics/oracle.md).

`BenchmarkStored` compares the canonicaliser with the test implementation;
milestone G1 records the baseline.
