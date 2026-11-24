package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// resultPollsTree is the sync tree of the recorded result-polls fixture,
// whose flow f/wl_rp/main polls the jobs of its own run.
var resultPollsTree = filepath.Join("..", "..", "conformance", "flows", "client-api", "result-polls", "windmill")

// TestHostResultPollsOption checks that the SDK's resultPollsForAllJobs
// reaches the engine (windmill-lite host -result-polls-for-all-jobs), on
// the real topology: the compiled test host connects with the option when
// WL_RESULT_POLLS_FOR_ALL_JOBS=1, and runs the result-polls fixture's
// flow, whose driver step polls a completed step, a failed sub-flow's flow
// job and its running sibling (conformance/flows/client-api/result-polls).
// With the option the run succeeds, and the driver reads the answers that
// the oracle recorded, such as the completed step's result in jsonb's text
// output. Without it, the control, the polls of those jobs answer 501,
// which fails the driver with UnsupportedPlatformCall.
func TestHostResultPollsOption(t *testing.T) {
	host := compiledHost(t)
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         resultPollsTree,
		EngineVersion:  resolveVersion(),
		Bun:            os.Getenv("WL_BUN"),
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out,
		Work:           filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	const completed = `{\"started\":true,\"success\":true,\"completed\":true,\"result\":{\"k\": [1, 2.5]}}`
	for _, on := range []bool{true, false} {
		var env []string
		if on {
			env = append(env, "WL_RESULT_POLLS_FOR_ALL_JOBS=1")
		}
		s := superviseHost(t, []string{host}, out, 30000, env...)
		r, err := s.run("f/wl_rp/main", "tenant-a", map[string]any{}, nil)
		if err != nil {
			t.Fatalf("resultPollsForAllJobs %t: %v", on, err)
		}
		result := string(r.Result)
		switch {
		case on && (r.Status != "success" || !strings.Contains(result, completed)):
			t.Errorf("with the option: %s %s, want success with %s", r.Status, result, completed)
		case !on && (r.Status != "failure" || !strings.Contains(result, "UnsupportedPlatformCall") || !strings.Contains(result, "get_result_maybe")):
			t.Errorf("without the option: %s %s, want the driver's UnsupportedPlatformCall for get_result_maybe", r.Status, result)
		}
	}
}
