//go:build wl_libc_malloc

package quickjs

// The build tag wl_libc_malloc makes the engine allocate its heap with the
// C library's malloc instead of mimalloc, so that CI can measure what the
// allocator choice is worth (README, Allocator). Releases never use it.

// #cgo CFLAGS: -DWL_LIBC_MALLOC
import "C"
