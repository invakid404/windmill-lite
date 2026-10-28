// Compiles the vendored mimalloc as part of this package (cgo builds only
// the C files of the package directory itself). The engine's heap uses it
// instead of the C library's malloc: musl's serialises every slot thread on
// one lock (see README, Allocator).
//
// musl defines no macro of its own. A Linux C library that is not glibc is
// musl here, the release build's (zig cc -target <arch>-linux-musl), and
// mimalloc's musl setting only stops it reading the thread pointer
// directly.
#ifndef WL_LIBC_MALLOC
#if defined(__linux__)
#include <features.h>
#if !defined(__GLIBC__)
#define MI_LIBC_MUSL 1
#endif
#endif
#include "mimalloc/src/static.c"
#endif
