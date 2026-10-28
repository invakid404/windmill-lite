//go:build !linux && !darwin

package oracle

import "errors"

// freeBytes is not implemented here; the disk policy then refuses to pull,
// because it fails closed (OR-30).
func freeBytes(string) (uint64, error) {
	return 0, errors.New("oracle: free-space measurement is supported on Linux and macOS only")
}
