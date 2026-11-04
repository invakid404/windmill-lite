package expr

import (
	"testing"
	"time"
)

// testEngine starts the real engine with limits l and slots slots (0: the
// default pool), and returns it with a function that stops it and reports
// a leaked heap block; quickjs_test.go registers it.
var testEngine func(l Limits, slots int) (Engine, func() error, error)

// testCLibrary is the C library the registered engine was built against.
var testCLibrary string

// RegisterTestEngine registers the real engine for the tests of this
// package.
func RegisterTestEngine(clib string, start func(l Limits, slots int) (Engine, func() error, error)) {
	testCLibrary, testEngine = clib, start
}

// realEngine starts the registered engine for the length of t.
func realEngine(t testing.TB, l Limits, slots int) Engine {
	t.Helper()
	if testEngine == nil {
		t.Fatal("no real engine is registered")
	}
	e, stop, err := testEngine(l, slots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})
	return e
}

// realLeakyEngine is realEngine for a test that drives QuickJS-ng into a
// leak of its own: it logs the heap blocks its runtimes leaked instead of
// failing. QuickJS-ng's JS_ToCStringLen2 keeps a reference to its source
// string when it fails to allocate the string's UTF-8 copy (quickjs.c,
// the two js_alloc_string failures that jump to fail), so a JSON.parse of
// a string with a non-ASCII character that runs out of the heap cap leaks
// that string's block; the engine reclaims it with the runtime's heap.
// TestJSExprKeepsTheHeapCap meets it by design.
func realLeakyEngine(t testing.TB, l Limits, slots int) Engine {
	t.Helper()
	if testEngine == nil {
		t.Fatal("no real engine is registered")
	}
	e, stop, err := testEngine(l, slots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Logf("%v (QuickJS-ng's out-of-memory leak in JS_ToCStringLen2)", err)
		}
	})
	return e
}

// setRetry sets p's JE-2 retries.
func setRetry(p *Pipeline, attempts int, pause time.Duration) {
	p.attempts, p.retryPause = attempts, pause
}
