//go:build !linux && !darwin

package main

import "errors"

// currentRSS reads the resident set size only on Linux and macOS.
func currentRSS() (int64, error) {
	return 0, errors.New("this engine reads its resident set size only on Linux and macOS")
}
