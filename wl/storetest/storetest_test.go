package storetest_test

import (
	"testing"

	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/storetest"
)

// TestMemoryResourceStore runs the contract suite on the memory store, the
// default store of a runtime.
func TestMemoryResourceStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) wl.ResourceStore { return wl.NewMemoryResourceStore() })
}
