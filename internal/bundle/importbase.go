package bundle

import "github.com/invakid404/windmill-lite/internal/ir"

// ImportBase is the import base of an inline flow script (WR-29): the path
// its relative imports resolve against, taken as a file, so that `./x`
// names a script beside it. It is the step's runnable path,
// `<flowPath>/<moduleID>` (the preprocessor's module id is "preprocessor"),
// at top level and in nested bodies alike, or explicitPath, the module's
// explicit `path`, where a deployed flow keeps it: in its failure and
// preprocessor slots. A deploy drops it from the inline scripts of the
// module lists, so for those the caller passes ""; internal/ir decides
// which in Script.ImportBase, the base Build uses. A path with a
// nested-body segment (`branchone-N`, `branchall-N`, `forloop-N`,
// `loop-N`) is collapsed to `<prefix>/flow`, so its imports resolve beside
// the prefix (ir.CollapseBodies).
//
// A path script's import base is its own path; it needs no function.
func ImportBase(flowPath, moduleID, explicitPath string) string {
	p := explicitPath
	if p == "" {
		p = flowPath + "/" + moduleID
	}
	return ir.CollapseBodies(p)
}
