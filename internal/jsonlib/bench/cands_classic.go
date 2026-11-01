//go:build !goexperiment.jsonv2

package bench

// v1Name is the v1 API's candidate name on the classic engine
// (GOEXPERIMENT=nojsonv2), where encoding/json/v2 does not exist.
const v1Name = "v1classic"

var v2Cands []Cand
