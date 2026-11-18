//go:build race

package main

// runLimitFactor multiplies the flow suite's per-run limit. Under the race
// detector a run that moves values of tens of MiB, such as
// client-api/state's size, spends most of its time in the race runtime's
// shadow memory for those buffers: 135 to 150 s against 6 to 15 s
// without it. The plain limit stays as it is.
const runLimitFactor = 3

// raceBuild reports a binary built with -race.
const raceBuild = true
