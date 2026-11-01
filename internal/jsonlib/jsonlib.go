// Package jsonlib is the JSON-library study's swap layer (branch
// study/json-libraries, never merged). The call sites on a step's path
// that scale with a payload call it instead of encoding/json, and a build
// tag picks the library behind it:
//
//	(none)             encoding/json, as today
//	jsonlib_v2         encoding/json/v2 and encoding/json/jsontext
//	jsonlib_sonic      github.com/bytedance/sonic, ConfigStd
//	jsonlib_sonicfast  github.com/bytedance/sonic, ConfigFastest
//
// Without a tag, GOEXPERIMENT=nojsonv2 gives the classic encoding/json
// engine instead of Go 1.27's default, the v2 implementation.
//
// Each function has its encoding/json namesake's contract:
//
//   - Valid is json.Valid.
//   - Unmarshal and Marshal are json.Unmarshal and json.Marshal.
//   - RawObject is json.Unmarshal into a map[string]json.RawMessage: an
//     object's members' texts, the last of duplicate keys kept.
//   - RawArray is json.Unmarshal into a []json.RawMessage.
//   - Members walks an object's members in order, duplicates included,
//     reporting each decoded key and where its key's text (quotes
//     included) and its value's text lie: obj[ks:ke] and obj[vs:ve]. It
//     is false for text that is not exactly one object.
package jsonlib
