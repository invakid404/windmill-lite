# wl/storetest

**Current state:** `storetest` holds the contract suite of
`wl.ResourceStore`, and the memory store passes it. This README covers
`wl/storetest/`, the Go package
`github.com/invakid404/windmill-lite/wl/storetest`.

A resource store keeps the resources that steps write through the job API,
such as the state of the stock client's `setState`
([The resource store](../README.md#the-resource-store)). A host that
implements its own store runs this suite on it from a Go test:

```go
func TestMyStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) wl.ResourceStore { return newMyStore(t) })
}
```

`Run` makes a new, empty store for each test with the function it gets.
Run the suite with `-race`: the atomicity tests look for data races as well
as for wrong answers.

## What the suite checks

| Test | What it checks |
|---|---|
| `MissingGet` | A path with no entry reads as missing, with no error. |
| `RoundTripIsByteExact` | A value comes back byte for byte: spacing, key order and number spelling. |
| `CreateAtTakenPath` | A create at a taken path reports `false` and changes nothing. |
| `UpdateKeepsTheType` | An update replaces the value and keeps the type. |
| `UpdateMissingCreatesNothing` | An update of a missing path reports `false` and creates nothing. |
| `Delete` | A delete removes the entry once; the path can then be created again. |
| `WorkspacesAreApart` | One path in two workspaces names two entries. |
| `Paths` | Paths with `/`, `:`, `%`, non-ASCII letters and 1 KiB are keys like any other. |
| `Values` | `null`, `{}` and a 32 MiB value are kept whole. |
| `ReturnedBytesAreTheCallers` | Changing the bytes passed in or returned changes no entry. |
| `CanceledContext` | With a canceled context, every operation returns an error and changes nothing. |
| `NoTornRead` | 8 writers and 8 readers on one path for 5 s: every read is missing or one whole value of 1 MiB that a writer wrote, checked by its SHA-256. |
| `CreateRace` | Of 64 creates at one path at once, exactly one reports `true`, and the entry holds its value. |
| `InterleavedWritesEndLegal` | Creates, updates, deletes and reads of one path in any order leave it missing or holding one whole written value. |

With `-short`, the two timed tests run for 1 s each. CI runs the suite
without `-short`.

## Work on storetest

| File | Contents |
|---|---|
| `storetest.go` | `Run`, `Names` and the tests |
| `storetest_test.go` | The suite on `wl.NewMemoryResourceStore` |
| `broken_test.go` | A store that writes an update in two steps and creates under two locks; the atomicity tests must fail on it |

Run `go test -race ./wl/storetest`. No test needs Bun.
