// Package bench holds the JSON-library study's microbenchmarks, peak-RSS
// probe and exactness probes (branch study/json-libraries, never merged).
// Every candidate runs in the same test binary, so one -bench run
// interleaves them case by case; the classic encoding/json engine needs its
// own binary, built with GOEXPERIMENT=nojsonv2.
package bench

import (
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/jsonlib/gje"
	"github.com/invakid404/windmill-lite/internal/jsonlib/sonicjson"
	"github.com/invakid404/windmill-lite/internal/jsonlib/std"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Cand is one candidate library's way of doing each operation; an
// operation it has no way of doing is nil.
type Cand struct {
	Name      string
	Valid     func([]byte) bool
	Compact   func(dst, b []byte) ([]byte, error)
	Unmarshal func([]byte, any) error
	Marshal   func(any) ([]byte, error)
	RawObject func([]byte) (map[string]json.RawMessage, error)
	RawArray  func([]byte) ([]json.RawMessage, error)
	Members   func([]byte, func(key string, ks, ke, vs, ve int)) bool
	Canonical func([]byte) ([]byte, error)
}

// Cands are the candidates this binary holds: encoding/json's v1 API (on
// the engine the build chose, v1Name), go-json-experiment, sonic in its two
// configurations, sonic/ast's lazy walk, and jsonval's canonicaliser; a
// default Go 1.27 build adds encoding/json/v2 and its RFC 8785 form
// (cands_v2.go).
var Cands = append([]Cand{
	{Name: v1Name, Valid: std.Valid, Compact: std.Compact, Unmarshal: std.Unmarshal, Marshal: std.Marshal, RawObject: std.RawObject, RawArray: std.RawArray, Members: std.Members, Canonical: std.Canonical},
	{Name: "gje", Valid: gje.Valid, Compact: gje.Compact, Unmarshal: gje.Unmarshal, Marshal: gje.Marshal, RawObject: gje.RawObject, RawArray: gje.RawArray, Members: gje.Members, Canonical: gje.Canonical},
	sonicCand(sonicjson.Std),
	sonicCand(sonicjson.Fastest),
	{Name: "sonicast", Members: sonicjson.Members},
	{Name: "jsonval", Canonical: func(b []byte) ([]byte, error) { return jsonval.FromProducer(b).Stored() }},
}, v2Cands...)

func sonicCand(l sonicjson.Lib) Cand {
	return Cand{Name: l.Name, Valid: l.Valid, Compact: l.Compact, Unmarshal: l.Unmarshal, Marshal: l.Marshal, RawObject: l.RawObject, RawArray: l.RawArray, Canonical: l.Canonical}
}
