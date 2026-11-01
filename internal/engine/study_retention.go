package engine

import (
	"sync"
	"weak"
)

// Study instrumentation for the JC1 retention probe (cmd/windmill-lite
// TestStudyRetention); the study branch only, never merged. It keeps a
// weak pointer to every run and its settlement, which does not keep them
// alive, so that the probe can count how many the collector has freed.
var studyRuns struct {
	mu      sync.Mutex
	runs    []weak.Pointer[run]
	settles []weak.Pointer[RunSettlement]
}

func studyTrack(r *run) {
	studyRuns.mu.Lock()
	defer studyRuns.mu.Unlock()
	studyRuns.runs = append(studyRuns.runs, weak.Make(r))
	studyRuns.settles = append(studyRuns.settles, weak.Make(r.settle))
}

// StudyRunsAlive reports how many of the runs started so far, and of their
// settlements, the collector has not freed, out of how many.
func StudyRunsAlive() (runs, settles, total int) {
	studyRuns.mu.Lock()
	defer studyRuns.mu.Unlock()
	for i, p := range studyRuns.runs {
		if p.Value() != nil {
			runs++
		}
		if studyRuns.settles[i].Value() != nil {
			settles++
		}
	}
	return runs, settles, len(studyRuns.runs)
}
