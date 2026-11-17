# internal/jsonval

**Current state:** the package holds the JSON value type, its `jsonb`
canonicaliser, the raw-text reads, the `jsonb` rejections, and the stored
size that the job reads use. It does not reproduce PostgreSQL's nesting
limit or `jsonb`'s size limits
([What is not reproduced from PostgreSQL](#what-is-not-reproduced-from-postgresql)).

This README covers `internal/jsonval`, with `LICENSE.PostgreSQL`, the
licence of the two files ported from PostgreSQL.

The package holds every JSON value at a step boundary as JSON text, as the
[JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md)
design record decides, and computes the text that PostgreSQL `jsonb` gives
back for it.

- **Callers:** [internal/engine](../engine/README.md) applies the design
  record's routing table with it, which decides whether each consumer sees
  the producer text or the stored text. [internal/harness](../harness/README.md),
  [internal/expr](../expr/README.md), [internal/platform](../platform/README.md),
  [wl](../../wl/README.md), `wltest` and the `windmill-lite` command hold
  values with it.
- **What it calls:** only the Go standard library.

## Files

| File | What it holds |
|---|---|
| `value.go` | `Value`, `FromProducer`, `FromPrinted`, `StripNUL` and the value's methods |
| `canonical.go` | The `jsonb` canonicaliser behind `Value.Stored` |
| `errors.go` | `StoreError` and the rejection errors |
| `raw.go` | The raw-text reads: `RawObject`, `RawArray`, `Members` |
| `size.go` | `StoredBound`, `ToastedSize` |
| `jsonb.go` | `jsonbDatum`: the `jsonb` datum that PostgreSQL builds (PostgreSQL licence) |
| `pglz.go` | `pglzCompressedLen`: pglz's compressed length (PostgreSQL licence) |

## Public surface

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Value` | struct | A JSON value as text ([The value type](#the-value-type)): `Producer`, `Stored`, `HasNUL`, `IsZero`, `WithoutNUL`, `String`, `MarshalJSON`, `UnmarshalJSON` | the engine, the harness, `internal/expr`, `internal/platform`, `wl`, `wltest`, `cmd/windmill-lite` |
| `FromProducer` | func | A `Value` of a producer's text, taken over without a copy | the engine, the harness, `internal/expr`, `internal/platform`, `wl`, `wltest` |
| `FromPrinted` | func | The `Value` that `FromProducer(printed)` would give, with its stored text already known: `printed` is a stored text with white space added outside its strings, as `jsonb` prints it. Neither text is copied or checked | `internal/expr` |
| `StoreError`, `ErrNUL`, `ErrUnpairedSurrogate`, `ErrNumericOverflow`, `ErrSyntax` | struct, vars | Why `Stored` failed ([Rejections](#rejections)); `StoreError.DatabaseMessage` gives PostgreSQL's message | the engine |
| `StripNUL` | func | Removes every NUL escape from a text, as Windmill does | the engine |
| `RawObject`, `RawArray` | funcs | An object's members, or an array's elements, as their texts ([Raw-text reads](#raw-text-reads)) | the harness, `wl`, the engine |
| `Members`, `Rest` | func, int type | Walks an object's members in place; what follows the object: `RestNone`, `RestValue`, `RestInvalid` | the engine, the harness |
| `StoredBound`, `ToastedSize` | funcs | The bound on a value's stored size, and its size after TOAST ([The stored size](#the-stored-size)) | the engine's job records |

## The value type

A `Value` holds every value as JSON text, and never decodes what it only
passes through. It has three parts:

| Part | When it is computed | Accessor |
|---|---|---|
| The producer text, byte for byte as a harness, QuickJS-ng, a static transform or the host wrote it | When the value is made (`FromProducer`) | `Producer` |
| The NUL flag: the text holds the escape `\u0000`, which PostgreSQL rejects ([OR-27](../../docs/semantics/oracle.md#or-27), [FX-48](../../docs/semantics/execution.md#fx-48)) | When the value is made | `HasNUL` |
| The stored text: the text that PostgreSQL `jsonb` gives back for the producer text ([FX-42](../../docs/semantics/execution.md#fx-42), [JE-44](../../docs/semantics/expressions.md#je-44)) | On first use, once | `Stored` |

- **Ownership.** `FromProducer` takes ownership of its byte slice, without
  copying or validating it. Callers must not modify what `Producer` or
  `Stored` return. `Stored` shares the producer's bytes when they are
  already in stored form.
- **Absent values.** The zero `Value` means no value at all. JSON `null`
  is a value.
- **JSON encoding.** `MarshalJSON` returns the producer text, and refuses
  the zero `Value`; tag optional fields `json:",omitzero"`.
- **What `encoding/json` changes.** `encoding/json` then compacts that text
  and escapes `<`, `>` and `&` (unless `SetEscapeHTML(false)`), which keeps
  the parsed value but not the bytes. So a harness frame or a recording,
  which must keep the producer's bytes, writes `Producer()` itself.
- **JSON decoding.** `UnmarshalJSON` keeps a copy of the raw text, so a
  value read from a frame keeps its spelling.
- **Concurrency.** A `Value` is immutable, and safe for concurrent use.

## The stored text

The canonicaliser makes one pass over the producer text, and builds no Go
values. Its output is `jsonb`'s text output without the spaces that `jsonb`
puts after `:` and `,`, which no consumer can see after parsing:

| Part of the text | Stored form |
|---|---|
| Object keys | Ordered by the decoded key's UTF-8 byte length, then bytewise. Of duplicate keys only the last is kept |
| Numbers | PostgreSQL `numeric` output: plain decimal with the exponent applied (`1e+21` → `1000000000000000000000`); `max(0, fraction digits − exponent)` digits after the point (`1.50e-3` → `0.00150`, `1.50e3` → `1500`); other digits kept (`1.50`); no sign on zero (`-0.0` → `0.0`) |
| Strings | Escapes decoded, then written as `jsonb` writes them: `\"`, `\\`, `\b`, `\f`, `\n`, `\r`, `\t`, other control characters as `\u00xx` with lowercase digits, and everything else, `/` and U+2028 included, as raw UTF-8 |
| White space | None |

- **A fixed point.** A `Value` made from stored text has that text as its
  stored text.
- **Cost.** One pass, plus a copy of each object whose keys are not
  already in order. An enclosing object that is reordered copies the inner
  bytes again. So the worst case is the text's size times the number of
  reordered objects on one path; Windmill fails context values nested
  deeper than 127 levels ([JE-49](../../docs/semantics/expressions.md#je-49)).
- **Deep nesting.** The canonicaliser keeps its own stack, so deep nesting
  cannot overflow Go's.

## Raw-text reads

`RawObject`, `RawArray` and `Members` read an object's members, or an
array's elements, as the texts that they are written as. They read with
`encoding/json/jsontext`, which reads the text in place; `encoding/json`
copies every value that it passes. Every other decode, and every marshal,
stays on `encoding/json`.

| Read | It is | Allocates |
|---|---|---|
| `RawObject` | `json.Unmarshal` into a `map[string]json.RawMessage`: the members' texts by decoded key, the last of duplicate keys kept | The map, and one copy of the members' texts |
| `RawArray` | `json.Unmarshal` into a `[]json.RawMessage` | The slice, and one copy of the elements' texts |
| `Members` | `encoding/json`'s token walk over an object: each member in order, duplicates included, with its decoded key and the offsets of its key's and value's texts, and what follows the object (`Rest`), as `Decoder.Token` reads it | The keys that hold escapes; the texts are slices of the input |

- **Who calls them:** the sites that keep a value as text. These are the
  harness's frame decode (`harness.Decode`) and `harness.scriptArgs`; the
  engine's member walk, behind `storeMembers`, `ManualFailure`, the flow
  input, identity results, the cache and loops; and `engine.Entrypoint`,
  `engine.elements` and the host's intercept map.
- **What they accept** is what `encoding/json` accepts: duplicate keys,
  invalid UTF-8 in strings, and nesting up to 10,000 levels. A key is
  decoded as `encoding/json` decodes it, with invalid UTF-8 and unpaired
  surrogates becoming U+FFFD.
- **Errors.** When a read fails, the text is read again with
  `encoding/json`, and its error is returned. So error texts stay
  `encoding/json`'s, and the same in every process: `encoding/json/v2`
  words a type mismatch at random once per process.
- **The Go build.** `encoding/json/jsontext` and `encoding/json/v2` are
  part of Go 1.27's default build. The engine does not build with
  `GOEXPERIMENT=nojsonv2`.

`TestEquivalence` holds the three reads to `encoding/json`'s map and slice
decodes and its token walks, on 106 probes, error texts included, and
fails on any difference. The probes hold numbers beyond `float64`, `-0`,
HTML, U+2028, invalid UTF-8, unpaired surrogates, control characters,
duplicate keys, trailing text and deep nesting.

## Rejections

`Stored` fails with a `*StoreError` when `jsonb` input rejects the text.

- **Which token.** For valid JSON text, it reports the first rejected token
  in text order, which is the one PostgreSQL reports.
- **Text that is not one JSON value** is always rejected, but the kind can
  differ from PostgreSQL's, whose parser reads one token ahead. It reports
  the NUL in `[1 "\u0000"]`, where `Stored` reports the syntax error.
  Producers never write such text.
- **The kind.** The error's `Kind` is one of four sentinels, so `errors.Is`
  works on it.
- **`Offset`** is the byte offset of the rejected escape inside its string,
  of the number, or of the unexpected character.
- **`DatabaseMessage`** gives PostgreSQL's message: the text that follows
  `error returned from database: ` in the error that Windmill reports when
  storing fails.

| Kind | The text holds | `DatabaseMessage` | Source |
|---|---|---|---|
| `ErrNUL` | The escape `\u0000` | `unsupported Unicode escape sequence` | [FX-48](../../docs/semantics/execution.md#fx-48); the message is recorded in [OR-27](../../docs/semantics/oracle.md#or-27) |
| `ErrUnpairedSurrogate` | A surrogate escape that is not half of a high-low pair | `invalid input syntax for type json` | [FX-51](../../docs/semantics/execution.md#fx-51) |
| `ErrNumericOverflow` | A number with more than 131072 digits before the point or 16383 after it, or an exponent beyond 2³⁰−1 in magnitude | `value overflows numeric format` | [FX-51](../../docs/semantics/execution.md#fx-51) for the digits; the exponent bound is PostgreSQL's |
| `ErrSyntax` | Anything that is not one JSON value in valid UTF-8 | `invalid input syntax for type json` | Producers never write it |

The engine routes these failures, at the boundaries where Windmill stores
the text ([internal/engine](../engine/README.md#failure-values)).

### NUL escapes

- **`HasNUL`** is the one rejection known without canonicalising, as the
  design record specifies.
- **Windmill removes every NUL escape** from a job's result and arguments
  before it stores them, so a NUL never reaches `jsonb` there
  ([OR-27](../../docs/semantics/oracle.md#or-27),
  [FX-48](../../docs/semantics/execution.md#fx-48)). It removes neither an
  unpaired surrogate nor an out-of-range number, so each fails where its
  text is stored ([FX-51](../../docs/semantics/execution.md#fx-51)).
- **`StripNUL`** removes them from a text as Windmill does: only a real
  escape, whose backslash is not itself escaped, in keys and values at any
  depth.
- **`WithoutNUL`** gives the `Value` of that text, or the same `Value` when
  it holds none. Of two keys that the removal makes equal, its stored text
  keeps the last, as `jsonb` does.
- **`Stored` still rejects a NUL,** as `jsonb` input does, and the engine
  strips the stored copy first at those two boundaries.

### What is not reproduced from PostgreSQL

- PostgreSQL's nesting limit, which depends on the server's stack size;
- `jsonb`'s limits on a string's length (256 MiB) and on a value's size;
- PostgreSQL's own message for invalid UTF-8, which never reaches a cast,
  because PostgreSQL refuses such text earlier.

## The stored size

Windmill's job read replaces a value whose stored column takes 90000
bytes or more, as `pg_column_size` measures it after TOAST compressed it
([OR-V1](../../docs/semantics/oracle.md#or-v1),
[WA-19](../../docs/semantics/client-api.md#wa-19)/o). The size is only the
threshold: the read gives arguments as `{"reason":"WINDMILL_TOO_BIG"}` and
a result as the string `"WINDMILL_TOO_BIG"`
([internal/engine](../engine/README.md#the-job-records)). The engine
decides in two steps:

1. **`StoredBound`** (`size.go`) bounds the bytes that `jsonb` stores a JSON
   text in, before any compression, so at least its `pg_column_size`
   however PostgreSQL stores it. When `StoredBound(text) < 90000`, the
   value is not replaced, with no further work.
2. **`ToastedSize`** (`size.go`) computes the size itself, for a value that
   TOAST moves out of line, as it does with any value near 90000 bytes.
   The value is replaced when it succeeds and gives 90000 or more. It
   is the compressed length and its 4-byte header when pglz's default
   strategy saves more than its 8-byte header and 2 bytes. Otherwise it is
   the datum's length, less its 4-byte header.

- **`jsonbDatum`** (`jsonb.go`) writes the exact `jsonb` datum that
  PostgreSQL 16 builds: entries, offsets every 32 entries, 4-byte alignment
  from the datum's start, numerics in their short or long form, keys
  ordered and deduplicated as `jsonb` orders them, little-endian.
- **`pglzCompressedLen`** (`pglz.go`) reproduces `pglz_compress` step for
  step, counting its output. Its hash reads bytes as x86-64's signed
  `char`, where the pinned oracle's PostgreSQL runs.
- **The licence.** Both files are ported from PostgreSQL, and keep its
  licence ([LICENSE.PostgreSQL](LICENSE.PostgreSQL)).
- **pglz, not lz4.** The oracle compresses with pglz. So a value that only
  lz4's 64 KiB window would shrink is replaced ([WA-19/o](../../docs/semantics/conformance.md#wa-19), run `window`).
- **The bound's terms.** The bound counts 8 bytes of headers and, for each
  value, key and container, 4 bytes of entry, up to 3 of alignment, and its
  content. The content is a string's bytes (at most its text's), a number's
  (at most 16 more than its text's), or 4 of a container's header.
- **A shortcut.** A byte of text takes at most 12 bytes stored, so a text
  under 7000 bytes is bounded without the walk.

## Tests

The tests need no Bun. The PostgreSQL tests need `WL_JSONB_PSQL` set to a
`psql` command line, and skip when it is not set; with it set and no
server reachable, they fail.

### Run the tests

1. Run the package's tests, as CI's Go jobs do:
   `go test -race ./internal/jsonval`
2. To compare with PostgreSQL itself, set `WL_JSONB_PSQL` to a `psql`
   command line that reaches a PostgreSQL 16 server, and run the three
   PostgreSQL tests, as CI's `jsonb (PostgreSQL)` job does:
   `go test -count=1 -v -run '^(TestStoredMatchesPostgreSQL|TestStoredBoundAbovePostgreSQL|TestToastedSizeMatchesPostgreSQL)$' ./internal/jsonval`
3. Fuzz the canonicaliser against the test implementation. Without
   `-fuzztime` the fuzzer runs until you stop it or it finds a failure;
   this form stops after 60 s:
   `go test -run '^$' -fuzz FuzzStored -fuzztime 60s ./internal/jsonval`
4. Run the benchmark: `go test -run '^$' -bench BenchmarkStored ./internal/jsonval`

CI runs the tests in `Go (ubuntu-latest)` and
`Go (macos-latest, packages)`, and the PostgreSQL tests in
`jsonb (PostgreSQL)`, against the oracle's PostgreSQL image, whose digest
[oracle.md](../../docs/semantics/oracle.md) pins
([docs/ci.md](../../docs/ci.md#the-ci-workflow)).

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `WL_JSONB_PSQL` | A `psql` command line, split on spaces, such as `docker exec -i <container> psql -U postgres` | `TestStoredMatchesPostgreSQL`, `TestStoredBoundAbovePostgreSQL`, `TestToastedSizeMatchesPostgreSQL` | The tests skip |

### Three checks of the canonicaliser

The design record checks the canonicaliser three ways, and all three run
in CI:

1. **The conformance cases** in
   [conformance/jsonb](../../conformance/README.md), which cite their
   catalogue cases and findings (`TestConformanceStored`).
   `TestConformanceStoredCatalogue` fails unless every catalogue case of
   each finding there is a case, or is deferred with a reason.
2. **The test implementation,** `referenceStored` in `reference_test.go`:
   an independent decode, sort and re-encode built on `encoding/json`,
   regular expressions and `math/big`, with no code shared with the
   canonicaliser. Edge cases, seeded generated text, the conformance cases
   and the fuzz target `FuzzStored` compare the two.
3. **PostgreSQL itself.** `TestStoredMatchesPostgreSQL` casts the inputs
   of the other tests, and mutations of the generated text, to `jsonb` and
   back, each distinct input once, except those that SQL text cannot
   carry (invalid UTF-8, a literal NUL byte). It compares the stored text and, for valid JSON, PostgreSQL's
   message. For text that is not one JSON value, it checks that both
   reject.

### Test files

| File | What it checks |
|---|---|
| `value_test.go` | The producer text kept verbatim; `HasNUL`; `WithoutNUL`; `Stored`, its shared bytes, and that it is computed once under concurrency; the zero `Value` is not `null`; JSON fields and what `encoding/json` respells; `UnmarshalJSON` copies its input |
| `canonical_test.go` | Edge cases; agreement with the test implementation on generated text (`FuzzStored` too); error offsets and `DatabaseMessage`; deep nesting. `TestSyntheticStored` and `BenchmarkStored` compare the canonicaliser with the test implementation on the step-result payloads of [internal/bench](../bench/README.md), from 26 B to 4 MiB; the baseline is in the [design record](../../docs/design/0005-json-step-boundaries.md#baseline) |
| `conformance_test.go` | `TestConformanceStored`, `TestConformanceStoredCatalogue` |
| `raw_test.go` | `TestEquivalence` ([Raw-text reads](#raw-text-reads)); `Members` reads in place |
| `postgres_test.go` | `TestStoredMatchesPostgreSQL` (needs `WL_JSONB_PSQL`) |
| `size_test.go` | `TestStoredBound`, `TestStoredBoundAbovePostgreSQL`, `TestToastedSizeMatchesPostgreSQL`, `TestToastedSizeAtTheReplacement` (below) |
| `reference_test.go` | The test implementation, `referenceStored` |

The size tests:

- **`TestStoredBound`** checks that the walk never gives more than the
  shortcut, 12 bytes per byte of text plus 32, and that `jsonb` stores every
  input.
- **`TestStoredBoundAbovePostgreSQL`** (with `WL_JSONB_PSQL`) checks that
  the bound is above `pg_column_size` of every input of the other tests
  that `jsonb` stores (`Stored` accepts it). It also covers texts that take
  `jsonb`'s most bytes per byte of text, as computed and as a table stores
  them.
- **What the job-reads-large fixture rests on,** which the same test
  checks: 89,000 characters that no compressor shrinks are stored below
  90000 bytes and bounded below, and 92,000 are stored above, while 92,000
  times `x` is stored below 90000 bytes.
- **`TestToastedSizeMatchesPostgreSQL`** (with `WL_JSONB_PSQL`) checks that
  the datum's length is `pg_column_size` of every input as computed. It
  checks that `ToastedSize` is the size that the table stores for every
  input large enough to be toasted, or the compressed length and its
  8-byte header for one that stays inline compressed. That happens only
  within TOAST's 2,032-byte target.
- **Its inputs** add texts that exercise pglz: arrays of objects, sentences
  over ASCII and multi-byte vocabularies, numbers, a block repeated inside
  and outside pglz's 4 KiB window, and random text with repeats near the
  25 % that pglz requires.
- **The boundary inputs:** results and argument objects stored in 89,999,
  90,000 and 90,001 bytes, either side of Windmill's replacement. They are
  made of random characters that pglz does not shrink, and of text that it
  shrinks to those sizes.
- **`TestToastedSizeAtTheReplacement`** checks the same sizes without
  PostgreSQL.
