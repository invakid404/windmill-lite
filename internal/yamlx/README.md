# internal/yamlx

**Current state:** implemented; nothing calls it yet. This README covers
`internal/yamlx`, its tests and `testdata/`. The package parses YAML into
the values the `wmill` CLI would send to Windmill: the YAML dialect of
[SY-9](../../docs/semantics/openflow.md#sy-9), as
[0002](../../docs/design/0002-ir-and-loader.md) (Pipeline, step 2) decides.
The sync-folder loader (`internal/syncfolder`, milestone A4) is its first
consumer and does not exist yet.

## Why a dialect

The CLI parses YAML with the npm `yaml` package v2 (YAML 1.2, core schema),
adds the scalar tags `!inline` and `!inline_fileset`, and sends the
resulting JavaScript values to the server as JSON. A Go YAML library
resolves scalars differently, so yamlx parses with `go.yaml.in/yaml/v3`
into nodes, ignores that library's scalar resolution, and resolves every
scalar itself.

## API

- `Parse(src) (*Node, error)` parses a stream holding at most one document.
  An empty stream, or one holding only comments, is null.
- `ToJSON(src) ([]byte, error)` returns the JSON text of the value, byte for
  byte what JavaScript's `JSON.stringify` gives for the CLI's value.
- `Node` has a `Kind` (`Null`, `Bool`, `Number`, `String`, `Sequence`,
  `Mapping`), `Text`, `Items`, `Pairs`, and a 1-based `Line` and `Column`
  (0 for the null of an empty stream).
  `Text` is the string, `"true"` or `"false"`, or a number exactly as
  written (`017`, `0x1F`, `.inf`): numbers keep their source text.
  `Float64()` gives a number's JavaScript value. `Get(key)` looks up a
  property. `MarshalJSON` and `AppendJSON` render `ToJSON`'s text;
  `encoding/json.Marshal` of a node changes the bytes (it escapes `<`, `>`,
  `&`, U+2028 and U+2029) but not the value.
- An alias shares the anchored `Node`, so nodes are read-only.
- Errors are `*Error` values with a `Kind` (`Syntax` or `Unsupported`), a
  position (0 where unknown) and a message; `Error()` reads
  `<kind>: line L column C: <message>` and leaves out what is unknown.

## The dialect

| Rule | Behaviour |
|---|---|
| Plain scalars | The YAML 1.2 core schema: null is `~`, `null`, `Null`, `NULL` or empty; booleans are `true`, `True`, `TRUE` and the three `false` forms; integers are `[-+]?[0-9]+`, `0o[0-7]+` and `0x[0-9a-fA-F]+`; floats are the decimal and exponent forms, `.inf` with an optional sign, and `.nan`. Everything else is a string: `yes`, `on`, `2024-01-01`, `1_000`, `0b11`, `0O17`, `-0x1F`. |
| Quoted and block scalars | Always strings. |
| Numbers | The JavaScript double the CLI computes: `017` is 17, `9007199254740993` is 9007199254740992, `1e400` is Infinity. In JSON, Infinity and NaN are `null`, `-0` is `0`, and the digits are JavaScript's (`1e+21`, `1e-7`, `0.000001`). |
| `!inline v`, `!inline_fileset v` | The string `!inline v` (the tag, a space, the scalar's text). On a sequence or mapping the tag is ignored. |
| Other tags | `!` makes a scalar a string. `!!str`, `!!int`, `!!float`, `!!bool` and `!!null` resolve when the text has the tag's form and are strings otherwise (`!!float 1` is `"1"`). `!!map` and `!!seq` leave a collection as it is and make a scalar a string. Unknown tags give the scalar's text as a string and leave a collection as it is. |
| Tag spelling | A tag is read as written in the source, because the library decodes percent escapes everywhere and the CLI does not. A `!!` shorthand has its escapes decoded (`!!%69nt` is `!!int`). A local tag (`!%69nline`) and a verbatim tag (`!<tag:yaml.org,2002:%69nt>`, `!<!!int>`) are taken literally, so they are unknown tags. |
| Keys | A key is the property name JavaScript's `String()` gives: `~` is `""`, `0x1F` is `"31"`, `.inf` is `"Infinity"`. `<<` is a literal key (YAML 1.2 has no merge). |
| Duplicate keys | Two scalar keys with equal values are an error (`1` and `0x1`, `~` and `null`). Distinct values with one property name (`1` and `"1"`, `true` and `"true"`) are not: the later value replaces the earlier one in place, as in a JavaScript object. Alias keys are never duplicates. |
| Property order | `Pairs` and the JSON follow JavaScript's property order: array-index keys (`0` to `4294967294`) in ascending order, then the other keys in first-seen order. |
| Aliases | Resolved to the anchored value. An alias inside its own anchor, or before its anchor, is an error. |
| Control and line-break characters | NEL, LS and PS (U+0085, U+2028, U+2029), DEL, the other C0 and C1 controls except tab, LF and CR, a U+FEFF after the start, U+FFFE and U+FFFF are ordinary characters, as the CLI's parser reads them. The YAML 1.1 parser underneath refuses them or reads them as line breaks, so yamlx hides them from it behind private-use characters the source does not use and puts them back in the parsed text. The CLI's writer emits DEL, LS, PS and U+FEFF unescaped. |
| Documents | One per stream; a second document, even an empty one, is an error. A leading byte order mark is dropped. |
| Block scalars | Literal and folded values are computed from the source with the CLI's rules, not taken from the library. An indentation indicator counts from the enclosing collection's column (0 at the root). A body without text is `""`, or with keep chomping one line break per blank line and at least one. Trailing blank lines indented beyond the first line of text are text. A last line that ends the file without a line break still gets one under clip and keep chomping. A first line of spaces and a tab, which the CLI's writer emits for text that starts with a tab, is read as the CLI reads it: the library refuses it, so yamlx hides that tab from it and parses again. |

## Refusals

yamlx refuses some YAML that the CLI accepts, because the CLI's value is
JavaScript-specific or because the YAML 1.1 parser underneath cannot read
it as the CLI does. These are restrictions of
[0001](../../docs/design/0001-equivalence-contract.md) §5a. Kind
`unsupported_yaml`:

- any directive except `%YAML 1.2`: the CLI reads `%YAML 1.1` with the
  YAML 1.1 schema (`yes` is true), applies `%TAG`, and ignores others;
- the tags `!!binary`, `!!timestamp`, `!!set`, `!!omap`, `!!pairs` and
  `!!merge`, which the CLI turns into a Buffer, a Date, a Set or a Map, or
  drops;
- a sequence or mapping as a mapping key, which the CLI turns into YAML
  text;
- a carriage return without a line feed, which the CLI reads as an ordinary
  character and the YAML 1.1 parser as a line break. The CLI's writer
  escapes it (`"\r"`);
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
  - a `?` with nothing but whitespace before a `,`.

  Quoting the scalar (`["?v"]`) avoids all three, and the CLI's writer
  emits flow style only for empty collections;
- aliases that add more than 2^22 to the document's weight (one per node
  plus the bytes of its scalars and keys). The CLI has its own guard
  instead: it refuses a document that uses one anchor 100 times or more,
  which yamlx does not reproduce;
- a source that is not valid UTF-8.

YAML 1.2 syntax that the YAML 1.1 parser rejects fails with the parser's
own `yaml_syntax` error: for example the `\/` escape, a `\u` escape naming
a lone surrogate, anchor names with characters other than letters, digits,
`-` and `_`, a tab after a sequence indicator, a tab inside a block
scalar's indentation after its first line, an empty implicit key (`: v`), a
percent escape in a tag that is not UTF-8 (`!%C0`), and a top-level block
scalar whose text starts at column 0.

The YAML 1.1 parser also accepts some malformed input the CLI rejects, such
as a quoted scalar continued at column 0 or `'v'# c`. The CLI cannot push
such a file, so there is no Windmill behaviour for it to match.

## Q-OF-6: what the library resolves differently

[Q-OF-6](../../docs/semantics/openflow.md#q-of-6) asks which scalar forms
the chosen Go library resolves differently from the CLI. With
`go.yaml.in/yaml/v3` decoding into an interface: `017` is 15, `1_000` is
1000, `0b11` is 3, `-0x1F` is -31, `0X1F` is 31, `! 017` is 15,
`2024-01-01` is a `time.Time`, `<<` merges, and integers above 2^53 keep
full precision. `library_test.go` checks each against the library and
against yamlx. yamlx uses none of that resolution.

## Tests

- `testdata/sy9.json` holds the cases, one per line: the YAML, and either
  the exact JSON text or an error substring. `spec` names the catalogue case
  (`SY-9/a` to `SY-9/c`, the milestone's exit gate) or the finding `SY-9`
  for cases that extend the catalogue. `policy` cases are the refusals;
  their `cli` field records what the CLI's parser returns for the same
  input. The file's `about` field states the provenance: every expected
  value was observed with npm `yaml` 2.8.3 as a black box, and none was
  recorded on a running Windmill.
- `conformance_test.go` runs the cases (`TestSY9Cases`) and checks that the
  catalogue's rows are present. `TestSY9CrossCheck` re-derives every
  expectation from npm `yaml`; it runs only when `YAMLX_NPM_YAML` names an
  unpacked package and `node` is on `PATH`:

  ```sh
  npm pack yaml@2.8.3 && tar xzf yaml-2.8.3.tgz
  YAMLX_NPM_YAML=$PWD/package go test -run TestSY9CrossCheck ./internal/yamlx
  ```

- `yamlx_test.go` covers the API: source text and values of numbers,
  positions, property order, shared alias nodes, the alias bound, block
  scalars whose first line starts with a tab, error kinds and positions,
  and JSON rendering.
- `FuzzToJSON` checks that any input gives a `*Error` or valid JSON, the
  same from `ToJSON` and `Parse`.
- `library_test.go` pins the Q-OF-6 answer above.
