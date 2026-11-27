# internal/yamlx

**Current state:** the package parses YAML into the values that the `wmill`
CLI would send to Windmill, and refuses the YAML that it cannot read as
the CLI does ([Refusals](#refusals)). The sync-folder loader, the
compiler's host contract and the oracle's tools call it.

This README covers `internal/yamlx`, its tests and its `testdata/`: the
cases in `sy9.json`.

The package parses YAML in the dialect of
[SY-9](../../docs/semantics/openflow.md#sy-9): what the CLI sends for a
sync folder's YAML file. It is step 2 of the loader pipeline of the
[IR, loader and validation](../../docs/design/0002-ir-and-loader.md)
design record.

- **Callers:** [internal/syncfolder](../syncfolder/README.md) parses every
  YAML file of a [sync folder](../../docs/glossary.md#sync-folder) with it;
  [internal/ir](../ir/README.md) parses the
  [host contract](../../docs/glossary.md#host-contract) with it (`ToJSON`);
  and [internal/oracle](../oracle/README.md) reads files with it.
- **What it calls:** the `go.yaml.in/yaml/v3` library, whose parser it
  uses and whose scalar resolution it does not.

## Why a dialect

The CLI parses YAML with the npm `yaml` package v2 (YAML 1.2, core
schema), adds the scalar tags `!inline` and `!inline_fileset`, and sends
the resulting JavaScript values to the server as JSON. A Go YAML library
resolves scalars differently. So yamlx parses with `go.yaml.in/yaml/v3`
into nodes, ignores that library's scalar resolution, and resolves every
scalar itself.

## Files

| File | What it holds |
|---|---|
| `yamlx.go` | `Parse`, `ToJSON`, `Node`, `Pair`, `Kind`, `Error`, `ErrorKind` |
| `parse.go` | The walk over the library's nodes: tags, anchors, aliases and their weight, keys |
| `resolve.go` | The YAML 1.2 core schema's resolution of plain and tagged scalars |
| `block.go` | Block scalars, read from the source |
| `flow.go` | The check of flow collections for the indicators that the two parsers read differently |
| `source.go` | The UTF-8 check, the characters and tabs hidden from the library, and `%YAML 1.2` |
| `json.go` | The JSON text: `JSON.stringify`'s strings and JavaScript's number format |

## Public surface

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Parse` | func | Parses a stream that holds at most one document into a `*Node`. An empty stream, or one that holds only comments, is null | `internal/syncfolder`, `internal/oracle` |
| `ToJSON` | func | Returns the JSON text of the value: byte for byte what JavaScript's `JSON.stringify` gives for the CLI's value | `internal/syncfolder`, `internal/ir`, `internal/oracle` |
| `Node`, `Pair` | structs | A parsed value, and one key and value of a mapping ([Nodes](#nodes)) | the callers of `Parse` |
| `Kind` | uint8 type | A node's kind: `Null`, `Bool`, `Number`, `String`, `Sequence`, `Mapping` | the same |
| `Error`, `ErrorKind` | struct, string type | Every failure is an `*Error`: a `Kind`, `Syntax` (`yaml_syntax`) or `Unsupported` (`unsupported_yaml`); a position (0 where it is unknown); and a message | the callers |

### Nodes

- **Fields.** A `Node` has a `Kind`, `Text`, `Items`, `Pairs`, and a
  1-based `Line` and `Column` (0 for the null of an empty stream).
- **`Text`** is the string, `"true"` or `"false"`, or a number exactly as
  written (`017`, `0x1F`, `.inf`): numbers keep their source text.
- **Methods.** `Float64` gives a number's JavaScript value. `Get(key)`
  looks up a property. `MarshalJSON` and `AppendJSON` render `ToJSON`'s
  text.
- **`encoding/json.Marshal`** of a node changes the bytes (it escapes `<`,
  `>`, `&`, U+2028 and U+2029), but not the value.
- **Read-only.** An alias shares the anchored `Node`, so nodes are
  read-only.
- **Errors.** `Error()` reads `<kind>: line L column C: <message>`, and
  leaves out what is unknown.

## The dialect

| Rule | Behaviour |
|---|---|
| Plain scalars | The YAML 1.2 core schema: null is `~`, `null`, `Null`, `NULL` or empty; booleans are `true`, `True`, `TRUE` and the three `false` forms. Integers are `[-+]?[0-9]+`, `0o[0-7]+` and `0x[0-9a-fA-F]+`; floats are the decimal and exponent forms, `.inf` with an optional sign, and `.nan`. Everything else is a string: `yes`, `on`, `2024-01-01`, `1_000`, `0b11`, `0O17`, `-0x1F` |
| Quoted and block scalars | Strings, unless a core tag resolves them: `!!int '017'` is 17, `!!null ''` is null |
| Numbers | The JavaScript double that the CLI computes: `017` is 17, `9007199254740993` is 9007199254740992, `1e400` is Infinity. In JSON, Infinity and NaN are `null`, `-0` is `0`, and the digits are JavaScript's (`1e+21`, `1e-7`, `0.000001`) |
| `!inline v`, `!inline_fileset v` | The string `!inline v` (the tag, a space, the scalar's text). On a sequence or a mapping the tag is ignored |
| Other tags | `!` makes a scalar a string. `!!str`, `!!int`, `!!float`, `!!bool` and `!!null` resolve when the text has the tag's form, and are strings otherwise (`!!float 1` is `"1"`). `!!map` and `!!seq` leave a collection as it is, and make a scalar a string. Unknown tags give the scalar's text as a string, and leave a collection as it is |
| Tag spelling | A tag is read as written in the source, because the library decodes percent escapes everywhere and the CLI does not. A `!!` shorthand has its escapes decoded (`!!%69nt` is `!!int`). A local tag (`!%69nline`) and a verbatim tag (`!<tag:yaml.org,2002:%69nt>`, `!<!!int>`) are taken literally, so they are unknown tags |
| Keys | A key is the property name that JavaScript's `String()` gives: `~` is `""`, `0x1F` is `"31"`, `.inf` is `"Infinity"`. `<<` is a literal key (YAML 1.2 has no merge) |
| Duplicate keys | Two scalar keys with equal values are an error (`1` and `0x1`, `~` and `null`). Distinct values with one property name (`1` and `"1"`, `true` and `"true"`) are not: the later value replaces the earlier one in place, as in a JavaScript object. Alias keys are never duplicates |
| Property order | `Pairs` and the JSON follow JavaScript's property order: array-index keys (`0` to `4294967294`) in ascending order, then the other keys in first-seen order |
| Aliases | Resolved to the anchored value. An alias inside its own anchor, or before its anchor, is an error |
| Documents | One per stream; a second document, even an empty one, is an error. A leading byte order mark is dropped |
| Control and line-break characters | Ordinary characters, as the CLI's parser reads them (below) |
| Block scalars | Computed from the source with the CLI's rules, not taken from the library (below) |

**Control and line-break characters.**

- These are ordinary characters, as the CLI's parser reads them:
  - NEL, LS and PS (`U+0085`, `U+2028`, `U+2029`), and DEL;
  - the other control characters (`U+0000` to `U+001F` and `U+0080` to
    `U+009F`), except tab, LF and CR;
  - a `U+FEFF` after the start, `U+FFFE` and `U+FFFF`.
- The YAML 1.1 parser underneath refuses them, or reads them as line
  breaks. So yamlx hides them from it behind private-use characters that
  the source does not use, and puts them back in the parsed text.
- The CLI's writer emits DEL, LS, PS and U+FEFF unescaped.

**Block scalars.** Literal and folded values are computed from the source
with the CLI's rules, not taken from the library:

- **The indentation indicator** counts from the column where the enclosing
  collection's content starts: its first key, `?` or `-`, after any tag or
  anchor of the collection on an earlier line (0 at the root).
- **A body without text** (only empty or blank lines) is `""`. Under keep
  chomping it is as many line breaks as the body holds, and at least one
  when the body has any line.
- **Trailing blank lines** indented beyond the first line of text are
  text.
- **A last line** that ends the file without a line break still gets one
  under clip and keep chomping.
- **A tab after spaces.** A first line of spaces and a tab, which the CLI's
  writer emits for text that starts with a tab, is read as the CLI reads
  it. The library refuses it, so yamlx hides that tab from it and parses
  again.
- **The header of such a block scalar,** one without an indentation
  indicator, is found with quotes skipped, so a key such as `'a # b'` does
  not hide it. A comment after it may follow a space or a tab.

## Refusals

yamlx refuses some YAML that the CLI accepts, because the CLI's value is
JavaScript-specific, or because the YAML 1.1 parser underneath cannot read
it as the CLI does. These are
[restrictions](../../docs/design/0001-equivalence-contract.md#5a-restrictions)
of the equivalence contract. Their kind is `unsupported_yaml`:

- any directive except `%YAML 1.2`: the CLI reads `%YAML 1.1` with the
  YAML 1.1 schema (`yes` is true), applies `%TAG`, and ignores others;
- the tags `!!binary`, `!!timestamp`, `!!set`, `!!omap`, `!!pairs` and
  `!!merge`, which the CLI turns into a Buffer, a Date, a Set or a Map, or
  drops;
- a sequence or a mapping as a mapping key, which the CLI turns into YAML
  text;
- a carriage return without a line feed, which the CLI reads as an
  ordinary character and the YAML 1.1 parser as a line break. The CLI's
  writer escapes it (`"\r"`);
- an anchor or alias name followed by a character that YAML 1.2 reads as
  part of the name, such as `&a:b`;
- a shorthand tag that contains a flow indicator (`,`, `[`, `]`, `{`,
  `}`);
- in a flow collection, a `?` or `:` that the two parsers read
  differently:
  - a `?` or `:` that starts a token and is followed by a plain character,
    as in `[?v]` or `[a :b]`. The CLI reads a plain scalar, the YAML 1.1
    parser an indicator;
  - a `:` after a plain scalar and before `,`, `]` or `}`, as in `[v:]`.
    The CLI reads an indicator, the YAML 1.1 parser part of the scalar;
  - a `?` with nothing but white space before a `,`.

  Quoting the scalar (`["?v"]`) avoids all three;
- aliases that add more than 2^22 to the document's weight: one per node,
  plus the bytes of its scalars and keys. The CLI has its own guard
  instead: it refuses a document that uses one anchor 100 times or more,
  which yamlx does not reproduce;
- a source that is not valid UTF-8.

YAML 1.2 syntax that the YAML 1.1 parser rejects fails with the parser's
own `yaml_syntax` error, for example:

- the `\/` escape, and a `\u` escape that names a lone surrogate;
- anchor names with characters other than letters, digits, `-` and `_`;
- a tab after a sequence indicator, and a tab inside a block scalar's
  indentation after its first line;
- an empty implicit key (`: v`);
- a top-level block scalar whose text starts at column 0, unless the
  library reads that text as a comment (`|` then `# c` is `"# c\n"`).

The cases in `testdata/sy9.json` pin these examples, except the failing
form of the last, which no case covers. The YAML 1.1 parser may refuse
other inputs that no case covers, and may accept malformed
input that the CLI rejects. The CLI cannot push such a file, so there is
no Windmill behaviour for it to match.

## What the library resolves differently

The reference asks which scalar forms the chosen Go library resolves
differently from the CLI
([Q-OF-6](../../docs/semantics/openflow.md#q-of-6)). With
`go.yaml.in/yaml/v3` decoding into an interface:

- `017` is 15, `1_000` is 1000, `0b11` is 3, `-0x1F` is -31, `0X1F` is 31,
  and `! 017` is 15;
- `2024-01-01` is a `time.Time`;
- `<<` merges;
- an integer above 2^53, such as `9007199254740993`, keeps every digit,
  where the CLI's JavaScript number rounds it to `9007199254740992`.

`library_test.go` checks each against the library and against yamlx.
yamlx uses none of that resolution.

## Tests

The tests need no Bun. The cross-check against npm `yaml` skips while
`YAMLX_NPM_YAML` is unset, and fails when the variable is set but Node or
npm `yaml` 2.8.3 is missing.

### Run the tests

1. Run the package's tests, as CI's Go jobs do:
   `go test -race ./internal/yamlx`
2. To cross-check every case against npm `yaml`, unpack the package in a
   directory of your own:
   `npm pack yaml@2.8.3 && tar xzf yaml-2.8.3.tgz`
3. From the repository root, run the cross-check with `YAMLX_NPM_YAML` set
   to the unpacked `package` directory, and `node` on `PATH`:
   `YAMLX_NPM_YAML=/path/to/dir/package go test -run TestSY9CrossCheck ./internal/yamlx`
4. Fuzz the parser. Without `-fuzztime` the fuzzer runs until you stop it
   or it finds a failure; this form stops after 60 s:
   `go test -run '^$' -fuzz FuzzToJSON -fuzztime 60s ./internal/yamlx`

CI runs the tests in the Go buckets of `ubuntu-latest` and
`macos-latest`: `FuzzToJSON` runs on its seed corpus, the
cases of `sy9.json`, but not with `-fuzz`, and the cross-check skips
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `YAMLX_NPM_YAML` | The directory of an unpacked npm `yaml` 2.8.3 package; another version fails the test | `TestSY9CrossCheck`, which also needs `node` on `PATH` | The test skips |

### The cases

`testdata/sy9.json` holds the cases, one per line: the YAML, and either
the exact JSON text or an error substring.

- **`spec`** names the catalogue case (`SY-9/a` to `SY-9/c`), or the finding
  `SY-9` for cases that extend the catalogue.
- **`policy` cases** are the refusals. Their `cli` field records what the
  CLI's parser returns for the same input.
- **Provenance.** The file's `about` field states it: every expected value
  was observed with npm `yaml` 2.8.3 as a black box, and none was recorded
  on a running Windmill.

### Test files

| File | What it checks |
|---|---|
| `conformance_test.go` | The cases (`TestSY9Cases`); that the catalogue's rows are present (`TestSY9CatalogueCovered`); `TestSY9CrossCheck` derives every expectation again from npm `yaml` |
| `yamlx_test.go` | The surface: the source text and the values of numbers, kinds and positions, property order, shared alias nodes, the alias bound, block scalars whose first line starts with a tab, error kinds and positions, and JSON rendering |
| `fuzz_test.go` | `FuzzToJSON`: any input gives a `*Error` or valid JSON, the same from `ToJSON` and from `Parse` |
| `library_test.go` | `TestQOF6LibraryResolution`: what the library resolves differently ([What the library resolves differently](#what-the-library-resolves-differently)) |
