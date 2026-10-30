package oracle

// The pinned oracle (OR-1, OR-2, OR-8). Images are referenced by tag and
// full index digest; both indexes carry linux/amd64 and linux/arm64.
const (
	// WindmillRepo is the CE image repository.
	WindmillRepo = "ghcr.io/windmill-labs/windmill"
	// WindmillDigest is the digest of the multi-arch index of 1.767.0.
	WindmillDigest = "sha256:9239264a7a849deedda87868147e65eb910456039fe9d961901e2e5b143ebd43"
	// WindmillImage is the full image reference the compose file uses.
	WindmillImage = WindmillRepo + ":1.767.0@" + WindmillDigest

	// PostgresRepo is the database image repository.
	PostgresRepo = "postgres"
	// PostgresDigest is the digest of the postgres:16-alpine index pinned
	// by OR-1.
	PostgresDigest = "sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"
	// PostgresImage is the full image reference the compose file uses.
	PostgresImage = PostgresRepo + ":16-alpine@" + PostgresDigest

	// OracleVersion is what GET /api/version must return, exactly (OR-1).
	OracleVersion = "CE v1.767.0"
	// CLIVersion is the version of the published windmill-cli package
	// the harness runs as a black-box tool (OR-8).
	CLIVersion = "1.767.0"
	// CLIPackage is the npm package spec of that CLI.
	CLIPackage = "windmill-cli@" + CLIVersion
	// BunVersion is the Bun inside the image (OR-2, WR-3).
	BunVersion = "1.3.10"

	// ProjectName is the compose project of an oracle session (OR-18).
	ProjectName = "wl-oracle"
	// BaseURL is where the oracle listens. It stays stable, because jobs
	// can observe it through WM_BASE_URL (OR-7).
	BaseURL = "http://localhost:8000"

	// AdminEmail and AdminPassword are Windmill's public default seed of
	// the superadmin, created by the first migration of a fresh database
	// (OR-6). They are not secrets: the database lives on tmpfs inside a
	// container bound to 127.0.0.1 for one session.
	AdminEmail    = "admin@windmill.dev"
	AdminPassword = "changeme"

	// CancelAfterReason is the reason a run's cancel_after_ms cancels it
	// with, and CancelAfterCanceler the canceler Windmill then names: the
	// superadmin's username (FX-38/a's recording). wltest check cancels
	// windmill-lite's run with the same two, so the Canceled results
	// compare.
	CancelAfterReason   = "wltest: cancel_after_ms"
	CancelAfterCanceler = "admin"
)

// SessionConfigs are the oracle configurations a session can run, each
// with the variables it adds to the oracle's environment (OR-7), which
// every recording's provenance holds. "" is the reference configuration
// of OR-4. A fixture names the configuration it needs
// (conformance.OracleNeeds.Session), and a session runs only the fixtures
// that name its own, as the recording requests of docs/design/README.md
// ask for the sessions with a changed configuration:
//   - native: WORKER_TAGS=nativets, which the standalone container's
//     default-group worker adds to its group's tags, so that it pulls
//     native jobs (Q-WR-21; FORCE_WORKER_TAGS would replace the tags);
//   - result_size: MAX_RESULT_SIZE_MB=3 (OR-7/b, Q-OR-14);
//   - flow_script_off: DISABLE_FLOW_SCRIPT=true (OF-V5/b, Q-OF-11);
//   - long: the reference configuration, for the fixtures whose runs wait
//     a minute or more on the server's own monitors, recorded in a session
//     of their own with a longer run timeout so that they cannot push the
//     main session past its time budget.
var SessionConfigs = map[string]map[string]string{
	"":                {},
	"native":          {"WORKER_TAGS": "nativets"},
	"result_size":     {"MAX_RESULT_SIZE_MB": "3"},
	"flow_script_off": {"DISABLE_FLOW_SCRIPT": "true"},
	"long":            {},
}
