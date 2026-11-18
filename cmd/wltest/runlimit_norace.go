//go:build !race

package main

// runLimitFactor multiplies the flow suite's per-run limit: 1 in a plain
// build (runlimit_race.go has why a -race build differs).
const runLimitFactor = 1

// raceBuild reports a binary built with -race.
const raceBuild = false
