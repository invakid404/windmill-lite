# internal/jsonval

**Current state:** implemented: the value type, the jsonb canonicaliser and
the raw-text reads.
The engine applies the routing table of
[ADR 0005](../../docs/design/0005-json-step-boundaries.md) with it. This
README covers `internal/jsonval`.

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
  `SetEscapeHTML(false)`), which keeps the parsed value but not the bytes.
  So a harness frame or a recording, which must keep the producer's bytes,
  writes `Producer()` itself.
- **JSON decoding.** `UnmarshalJSON` keeps a copy of the raw text, so a
  value read from a frame keeps its spelling.
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

## Raw-text reads

`RawObject`, `RawArray` and `Members` read an object's members or an
array's elements as the texts they are written as. They read with
`encoding/json/jsontext`, which reads the text in place; `encoding/json`
copies every value it passes. Every other decode and every marshal stays
on `encoding/json`.

The sites that keep a value as text call them:

- the harness's frame decode (`harness.Decode`) and `harness.scriptArgs`;
- the engine's member walk, behind `storeMembers`, `ManualFailure`, the
  flow input, identity results, the cache and loops;
- `engine.Entrypoint`, `engine.elements` and the host's intercept map.

| Read | It is | Allocates |
|---|---|---|
| `RawObject` | `json.Unmarshal` into a `map[string]json.RawMessage`: the members' texts by decoded key, the last of duplicate keys kept | the map and one copy of the members' texts |
| `RawArray` | `json.Unmarshal` into a `[]json.RawMessage` | the slice and one copy of the elements' texts |
| `Members` | `encoding/json`'s token walk over an object: each member in order, duplicates included, its decoded key and the offsets of its key's and value's texts, and what follows the object (`Rest`), as `Decoder.Token` reads it | the keys that hold escapes; the texts are slices of the input |

They accept what `encoding/json` accepts: duplicate keys, invalid UTF-8
in strings, and nesting up to 10,000 levels. A key is decoded as
`encoding/json` decodes it, invalid UTF-8 and unpaired surrogates becoming
U+FFFD.

When a read fails, the text is read again with `encoding/json` and its
error is returned. So error texts stay `encoding/json`'s, and the same in
every process: `encoding/json/v2` words a type mismatch at random once per
process.

`TestEquivalence` holds the three reads to `encoding/json`'s map and slice
decodes and its token walks on 106 probes, error texts included, and fails
on any difference. The probes hold numbers beyond `float64`, `-0`, HTML,
U+2028, invalid UTF-8, unpaired surrogates, control characters, duplicate
keys, trailing text and deep nesting.

`encoding/json/jsontext` and `encoding/json/v2` are part of Go 1.27's
default build; the engine does not build with `GOEXPERIMENT=nojsonv2`.

## Rejections

`Stored` fails with a `*StoreError` when `jsonb` input rejects the text. For
valid JSON text it reports the first rejected token in text order, which is
the one PostgreSQL reports. Text that is not one JSON value is always
rejected, but the kind can differ from PostgreSQL's, whose parser reads one
token ahead (it reports the NUL in `[1 "\u0000"]`, where `Stored` reports the
syntax error); producers never write such text.

The error's `Kind` is one of four sentinels, so `errors.Is` works on it. Its
`Offset` is the byte offset of the rejected escape inside its string, of the
number, or of the unexpected character. `DatabaseMessage` gives PostgreSQL's
message: the text that follows `error returned from database: ` in the error
Windmill reports when storing fails. The engine routes these failures as
FX-48 says.

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
stack size; and `jsonb`'s limits on a string's length (256 MiB) and on a
value's size. Nor is PostgreSQL's own message for invalid UTF-8, which
never reaches a cast because PostgreSQL refuses such text earlier.

## The stored size

Windmill's job read replaces a value whose stored column takes 90000
bytes or more by `pg_column_size`, after TOAST compressed it
([OR-V1](../../docs/semantics/oracle.md#or-v1),
[WA-19](../../docs/semantics/client-api.md#wa-19)/o). The job reads
reproduce that size in two steps.

`StoredBound` (`size.go`) bounds the bytes that `jsonb` stores a JSON text
in, before any compression, so above its `pg_column_size` however
PostgreSQL stores it: a value below the bound is never replaced.

`ToastedSize` (`size.go`) computes the size itself, for a value TOAST
moves out of line, as it does with any value near 90000 bytes: the
compressed length and its 4-byte header when pglz's default strategy
saves more than its 8-byte header and 2 bytes, else the datum's length
less its 4-byte header. `jsonbDatum` (`jsonb.go`) writes the exact jsonb
datum PostgreSQL 16 builds: entries, offsets every 32 entries, 4-byte
alignment from the datum's start, numerics in their short or long form,
keys ordered and deduplicated as jsonb orders them, little-endian.
`pglzCompressedLen` (`pglz.go`) reproduces `pglz_compress` step for step,
counting its output, with the hash reading bytes as x86-64's signed
`char`, where the pinned oracle's PostgreSQL runs. Both files are ported
from PostgreSQL and keep its licence
([LICENSE.PostgreSQL](LICENSE.PostgreSQL)). The oracle compresses with
pglz, not lz4: a value only lz4's 64 KiB window would shrink is replaced
(WA-19/o, run `window`).

The bound counts 8 bytes of headers and, for each value, key and
container, 4 bytes of entry, up to 3 of alignment and its content: a
string's bytes, at most its text's; a number's, at most 16 more than its
text's; 4 of a container's header. A byte of text takes at most 12 bytes
stored, so a text under 7000 bytes is bounded without the walk.
`TestStoredBound` checks that the walk stays under that shortcut, and
`TestStoredBoundAbovePostgreSQL` (run as `TestStoredMatchesPostgreSQL`
is, below) that the bound is above `pg_column_size` of every input of
the other tests that `jsonb` stores (`Stored` accepts it) and of texts
that take `jsonb`'s most bytes per byte of text, as computed and as a
table stores them. It also checks what the
job-reads-large fixture rests on: 89,000 characters that no compressor
shrinks are stored below 90000 bytes and bounded below, and 92,000 are
stored above, while 92,000 times `x` is stored far below.
`TestToastedSizeMatchesPostgreSQL`, run the same way, checks that the
datum's length is `pg_column_size` of every input as computed, and that
`ToastedSize` is the size the table stores for every input large enough
to be toasted, or the compressed length and its 8-byte header for one
that stays inline compressed. Its inputs add texts that exercise pglz:
arrays of objects, sentences over ASCII and multi-byte vocabularies,
numbers, a block repeated inside and outside pglz's 4 KiB window, and
random text with repeats near the 25 % pglz requires.

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
   the other tests, and mutations of the generated text, to `jsonb` and back.
   It compares the stored text and, for valid JSON, PostgreSQL's message; for
   text that is not one JSON value it checks that both reject. It runs only
   when `WL_JSONB_PSQL` holds a `psql` command line (split on spaces), which
   CI's `jsonb (PostgreSQL)` job points at the oracle's PostgreSQL image
   ([oracle.md](../../docs/semantics/oracle.md) pins its digest).

`BenchmarkStored` compares the canonicaliser with the test implementation
on the step-result-shaped payloads of
[internal/bench](../bench/README.md), from 26 B to 4 MiB, and
`TestSyntheticStored` checks that the two agree on each of them. The
baseline is in
[ADR 0005](../../docs/design/0005-json-step-boundaries.md#baseline).
