package oracle

import (
	"github.com/invakid404/windmill-lite/internal/conformance"
)

// checkReplay holds a stub run's answered calls against its walk with the
// rules windmill-lite's runner applies to its trace
// (conformance.CheckReplay): the walk's ids are where each job was walked,
// and the jobs of the kinds that execute a script must each have called.
func checkReplay(w *Walk, calls []conformance.ReplayCall) error {
	var scripts []string
	for id, site := range w.IDs {
		if scriptKinds[w.Timings[site.String()].Kind] {
			scripts = append(scripts, id)
		}
	}
	return conformance.CheckReplay(calls, w.IDs, scripts)
}
