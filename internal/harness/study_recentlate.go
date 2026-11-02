package harness

// studyRecentLate, when above zero, replaces DefaultRecentLate: the
// control of the JC1 retention probe (cmd/windmill-lite
// TestStudyRetention), on the study branch only, never merged. It is zero
// in every build but the study_recentlate2 tag's, so the default is
// unchanged.
var studyRecentLate int
