//go:build goexperiment.jsonv2

package bench

import v2 "github.com/invakid404/windmill-lite/internal/jsonlib/v2"

// v1Name is the v1 API's candidate name: on Go 1.27's default engine, the
// v2 implementation, it is what windmill-lite runs today.
const v1Name = "v1"

var v2Cands = []Cand{
	{Name: "v2", Valid: v2.Valid, Compact: v2.Compact, Unmarshal: v2.Unmarshal, Marshal: v2.Marshal, RawObject: v2.RawObject, Members: v2.Members, Canonical: v2.Canonical},
	{Name: "jcs", Canonical: v2.JCS},
}
