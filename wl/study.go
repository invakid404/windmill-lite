package wl

import "github.com/invakid404/windmill-lite/internal/jsonlib"

// StudyRunEndPayload is the on_run_end host_call payload of t, marshalled
// as the plugin's onRunEnd marshals it (doHeld), for the JSON-library
// study's benchmarks (branch study/json-libraries, never merged).
func StudyRunEndPayload(t *Trace) ([]byte, error) {
	return jsonlib.Marshal(map[string]any{"trace": traceJSON(t)})
}
