//go:build race

package racebuild

// Enabled reports a binary built with -race.
const Enabled = true

// Factor multiplies the time limits that a run of tens of MiB meets under
// the race detector, whose runtime keeps shadow memory for every copy of
// such a buffer: the flow suite's per-run limit and the harness watchdog's
// default silence threshold. Measured on client-api/state's size run, the
// 32 MiB platform request takes 11 to 15 s to decode under -race, which
// the watchdog counts as the harness's silence, and the run 135 to 150 s,
// against 6 to 15 s without it.
const Factor = 3
