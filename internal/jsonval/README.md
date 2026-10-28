# internal/jsonval

**Current state:** the value type of milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)). This README
covers `internal/jsonval`. `Value` and its NUL flag exist and are tested. The
jsonb canonicaliser is milestone A1, so until it lands `Stored` returns
`ErrNotCanonicalised` for every value without a NUL.

## The value type

[ADR 0005](../../docs/design/0005-json-step-boundaries.md) holds every value
as JSON text and never decodes what it only passes through. A `Value` has
three parts:

| Part | When it is computed | Accessor |
|---|---|---|
| the producer text, byte for byte as a harness, QuickJS-ng, a static transform or the host wrote it | when the value is made (`FromProducer`) | `Producer` |
| the NUL flag: the text holds the escape `\u0000`, which PostgreSQL rejects ([OR-27](../../docs/semantics/oracle.md#or-27), [FX-48](../../docs/semantics/execution.md#fx-48)) | when the value is made | `HasNUL` |
| the stored text, the jsonb-canonical form ([FX-42](../../docs/semantics/execution.md#fx-42)) | on first use, once | `Stored` (`ErrNUL` for a NUL value) |

- **Ownership.** `FromProducer` takes ownership of its byte slice without
  copying or validating it.
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
