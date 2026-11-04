package main

import "os"

// WL_TEST_QUICKJS_ONLY=1 runs this package's flow, step and expression
// suites with the Go expression evaluator off (wl.Options.QuickJSOnly), so
// that the conformance cases and their recordings pass on QuickJS alone
// too; CI runs them both ways.
func init() {
	if os.Getenv("WL_TEST_QUICKJS_ONLY") == "1" {
		quickJSOnlyAll = true
	}
}
