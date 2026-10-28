package bundle

import (
	"regexp"
	"strings"
)

// bodySegment matches a path segment that names a nested flow body (WR-29).
var bodySegment = regexp.MustCompile(`^(branchone|branchall|forloop|loop)-[0-9]+$`)

// ImportBase is the import base of an inline flow script (WR-29): the path
// its relative imports resolve against, taken as a file, so that `./x`
// names a script beside it. It is the step's runnable path,
// `<flowPath>/<moduleID>` (the preprocessor's module id is "preprocessor"),
// at top level and in nested bodies alike, or the module's explicit `path`
// when it sets one. A path with a nested-body segment (`branchone-N`,
// `branchall-N`, `forloop-N`, `loop-N`) is collapsed to `<prefix>/flow`,
// so its imports resolve beside the prefix.
//
// A path script's import base is its own path; it needs no function.
func ImportBase(flowPath, moduleID, explicitPath string) string {
	p := explicitPath
	if p == "" {
		p = flowPath + "/" + moduleID
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if bodySegment.MatchString(s) {
			return strings.Join(append(segs[:i:i], "flow"), "/")
		}
	}
	return p
}
