package engine

import (
	"errors"
	"os"
	"strings"

	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// laterBunVariables are the variables Windmill's worker sets in a Bun job's
// environment after the script's own envs, so that one of these names in
// envs never reaches the job (WR-22, WA-2): the reserved variables, which
// it always sets (empty when one does not apply), and the common Bun
// variables, set last. NODE_PATH is one of them only when the engine's
// own environment has it, as the worker copies its own (workerSets);
// BUN_INSTALL_MINIMUM_RELEASE_AGE is set only when the worker configures
// it, which windmill-lite's harness does not.
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

// workerSets reports whether the worker sets the variable k after a
// script's own envs: one of laterBunVariables, or NODE_PATH when the
// engine has one, which the harness passes on (WR-22/b, harness.BaseEnv).
func workerSets(k string) bool {
	if k == "NODE_PATH" {
		_, ok := os.LookupEnv("NODE_PATH")
		return ok
	}
	return laterBunVariables[k]
}

// addScriptEnvs adds a path script's own envs, the KEY=VALUE entries of
// its metadata, to the environment of a Bun invocation of it (WR-22, item
// 1 of its assembly order). Each entry is split at its first "=", with
// nothing trimmed, so an empty name is kept; every other source overrides
// them, so a name the worker sets later, or one env already holds, is
// skipped, and among the entries a later one wins. A native invocation
// gets none of them: the native runtime's process.env holds only the base
// URLs and the reserved variables (WR-45).
//
// The worker reads the entries for every job of the common language
// dispatch, a bunnative one's included, before it resolves the job's
// arguments: an entry with no "=" fails the job before it runs, which the
// returned error says (envError). A nativets job is dispatched before
// that step, and its entries are never read (WR-22).
func addScriptEnvs(env map[string]string, s *ir.Script, b Boundary) error {
	if s == nil || s.Metadata == nil || s.Language == openflow.LangNativeTS {
		return nil
	}
	own := map[string]string{}
	for _, e := range s.Metadata.Envs {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			return envError(e)
		}
		if !workerSets(k) {
			own[k] = v
		}
	}
	if b != BoundaryBun {
		return nil
	}
	for k, v := range own {
		if _, set := env[k]; !set {
			env[k] = v
		}
	}
	return nil
}

// envError is the worker's refusal of an envs entry with no "=" (WR-22),
// a bad request that the job's execution error quotes after its
// "execution error:\n" lead-in (WR-37).
func envError(entry string) error {
	return errors.New("Bad request: Invalid env var: " + entry + ". Must be in the form of KEY=VALUE")
}
