//go:build !race

// Package racebuild tells the engine and its test tooling whether the
// binary was built with -race, and by how much to stretch the time limits
// that the race detector's runtime makes runs of large values exceed
// (race.go has the measurements). A plain build keeps every limit as it
// is: Factor is 1.
package racebuild

// Enabled reports a binary built with -race.
const Enabled = false

// Factor multiplies the race-sensitive time limits: 1 in a plain build.
const Factor = 1
