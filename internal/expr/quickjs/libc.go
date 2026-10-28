package quickjs

/*
// Any C library header defines __GLIBC__ on glibc; musl defines no macro
// of its own.
#include <stdlib.h>

static const char *wl_libc(void)
{
#if defined(__GLIBC__)
	return "glibc";
#elif defined(__APPLE__)
	return "apple";
#elif defined(__linux__)
	return "musl";
#else
	return "other";
#endif
}
*/
import "C"

// CLibrary names the C library the engine was built against: "glibc",
// "musl" (the static Linux engine), "apple" or "other". QuickJS-ng's
// transcendental Math functions call that library's libm, so their last
// digit can differ from the glibc of Windmill's image (deviation D16 of
// ADR 0001).
func CLibrary() string { return C.GoString(C.wl_libc()) }
