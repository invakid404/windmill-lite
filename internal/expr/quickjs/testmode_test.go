package quickjs

import "os"

// testOptions is opts as the tests' engines take them: WL_QJS_LEAK_CHECK=1
// turns on leak check (Options.LeakCheck) for every engine newEngine and
// newLeakyEngine make, so that newEngine's check that no runtime leaked
// still fails a test when QuickJS-ng starts leaking. CI runs the
// expression packages so once.
func testOptions(opts Options) Options {
	if os.Getenv("WL_QJS_LEAK_CHECK") == "1" {
		opts.LeakCheck = true
	}
	return opts
}
