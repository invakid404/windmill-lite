# internal/racebuild

**Current state:** two constants that say whether the binary was built
with `-race`, and by how much to stretch the time limits that the race
detector makes large runs exceed. This README covers
`internal/racebuild`, the Go package
`github.com/invakid404/windmill-lite/internal/racebuild`.

| Constant | Plain build | Built with `-race` |
|---|---|---|
| `Enabled` | `false` | `true` |
| `Factor` | `1` | `3` |

Under the race detector, a run that moves values of tens of MiB is about
ten times slower: the race runtime keeps shadow memory for every copy of
such a buffer. Two limits read `Factor`:

- the flow suite's per-run limit in [cmd/wltest](../../cmd/wltest/README.md),
  150 s in a plain build;
- the harness watchdog's default silence threshold
  (`harness.DefaultKillAfter`), 30 s in a plain build. The engine decodes a
  frame from the harness before it reads the next one, and under `-race`
  a 32 MiB platform request takes 11 to 15 s to decode, which the
  watchdog counts as the harness's silence.

A plain build keeps both limits exactly. `TestFlowRunTimeout`
(cmd/wltest) checks both values in each kind of build.

## Files

| File | Contents |
|---|---|
| `race.go` | The constants of a `-race` build (`//go:build race`), with the measurements |
| `norace.go` | The package comment and the constants of a plain build |
