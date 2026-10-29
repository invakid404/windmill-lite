package engine

import (
	"strings"

	"github.com/invakid404/windmill-lite/internal/ir"
)

// laterBunVariables are the variables Windmill's worker sets in a Bun job's
// environment after the script's own envs, so that one of these names in
// envs never reaches the job (WR-22, WA-2): the reserved variables, which
// it always sets (empty when one does not apply), and the common Bun
// variables, set last. NODE_PATH and BUN_INSTALL_MINIMUM_RELEASE_AGE are
// set only when the worker configures them, which windmill-lite's harness
// does not, so they are not among them.
var laterBunVariables = map[string]bool{
	// The reserved variables.
	"WM_WORKSPACE": true, "WM_EMAIL": true, "WM_USERNAME": true, "WM_PERMISSIONED_AS": true,
	"WM_TOKEN": true, "WM_BASE_URL": true, "WM_JOB_ID": true, "WM_JOB_PATH": true,
	"WM_FLOW_JOB_ID": true, "WM_ROOT_FLOW_JOB_ID": true, "WM_ROOT_JOB_ID": true, "WM_FLOW_PATH": true,
	"WM_FLOW_STEP_ID": true, "WM_SCHEDULE_PATH": true, "WM_SCHEDULED_FOR": true, "WM_STATE_PATH": true,
	"WM_OBJECT_PATH": true, "WM_WORKER_GROUP": true, "WM_RUNNABLE_ID": true, "WM_END_USER_EMAIL": true,
	"WM_TESTED_RUNNABLE": true,
	// The common Bun variables.
	"PATH": true, "HOME": true, "TZ": true, "FORCE_COLOR": true, "DO_NOT_TRACK": true,
	"BUN_INSTALL_CACHE_DIR": true, "BUN_RUNTIME_TRANSPILER_CACHE_PATH": true, "BASE_URL": true,
}

// addScriptEnvs adds a path script's own envs, the KEY=VALUE entries of
// its metadata, to the environment of a Bun invocation of it (WR-22, item
// 1 of its assembly order): every other source overrides them, so a name
// the worker sets later, or one env already holds, is skipped, and among
// the entries a later one wins. An entry with no "=", or with an empty
// name, sets nothing. A native invocation gets none of them: the native
// runtime's process.env holds only the base URLs and the reserved
// variables (WR-45).
func addScriptEnvs(env map[string]string, s *ir.Script, b Boundary) {
	if s == nil || s.Metadata == nil || b != BoundaryBun {
		return
	}
	own := map[string]string{}
	for _, e := range s.Metadata.Envs {
		if k, v, ok := strings.Cut(e, "="); ok && k != "" && !laterBunVariables[k] {
			own[k] = v
		}
	}
	for k, v := range own {
		if _, set := env[k]; !set {
			env[k] = v
		}
	}
}
