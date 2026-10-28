package engine_test

import (
	"fmt"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
)

// haltBeforeWrite halts runs before one site and keeps every other default.
type haltBeforeWrite struct {
	engine.NoHooks
	site conformance.SitePath
}

func (h haltBeforeWrite) HaltBefore(s engine.Site) bool { return s.Path == h.site }

// A host implements the hooks it needs by embedding NoHooks; the engine then
// applies its signal through the precedence functions.
func ExampleNoHooks() {
	write := conformance.SitePath{}.Child(conformance.Module("write"))
	var hooks engine.Hooks = haltBeforeWrite{site: write}

	for _, site := range []engine.Site{
		{Path: conformance.SitePath{}.Child(conformance.Module("read"))},
		{Path: write},
	} {
		v := engine.Start(engine.StartSignals{HaltBefore: hooks.HaltBefore(site)})
		fmt.Println(site.Path, v)
	}
	// Output:
	// m:read proceed
	// m:write halted
}
