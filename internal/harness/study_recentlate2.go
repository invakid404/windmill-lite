//go:build study_recentlate2

package harness

// The JC1 retention probe's control build: each harness process keeps its
// last 2 settled invocations instead of DefaultRecentLate.
func init() { studyRecentLate = 2 }
