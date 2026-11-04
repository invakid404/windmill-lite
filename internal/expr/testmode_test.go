package expr

import (
	"os"
	"testing"
)

// WL_TEST_QUICKJS_ONLY=1 runs this package's tests with jsExpr off in every
// pipeline they make (SetQuickJSOnly), so that the expression cases and
// their recordings pass on QuickJS alone too; CI runs them both ways.
func init() {
	if os.Getenv("WL_TEST_QUICKJS_ONLY") == "1" {
		quickJSOnlyDefault = true
	}
}

// TestQuickJSOnlyTestMode checks that WL_TEST_QUICKJS_ONLY reaches the
// pipelines this package's tests make, so that CI's QuickJS-only part does
// run them without jsExpr.
func TestQuickJSOnlyTestMode(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 1))
	if want := os.Getenv("WL_TEST_QUICKJS_ONLY") != "1"; p.js != want {
		t.Fatalf("jsExpr on: %t, want %t", p.js, want)
	}
}
