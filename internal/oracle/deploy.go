package oracle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// Boot is the outcome of waiting for a fresh oracle.
type Boot struct {
	// VersionAfter and HealthyAfter are the times from the start of the
	// wait to the first 200 of /api/version and to a healthy status with
	// enough workers (OR-4/a).
	VersionAfter, HealthyAfter time.Duration
	// Health is the healthy status.
	Health Health
}

// WaitReady follows the boot order of OR-6: /api/version answers, its text
// is exactly OracleVersion (otherwise ErrVersionMismatch, OR-1/b), and
// /api/health/status?force=true reports healthy with at least workers
// workers alive. It polls every interval until timeout (OR-4/a allows
// 180 s).
func WaitReady(ctx context.Context, c *Client, workers int, interval, timeout time.Duration) (Boot, error) {
	var b Boot
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var version string
	if err := poll(ctx, interval, func() (bool, error) {
		v, err := c.Version(ctx)
		if err != nil {
			return false, nil // not up yet
		}
		version = v
		return true, nil
	}); err != nil {
		return b, fmt.Errorf("oracle: /api/version did not answer within %s: %w", timeout, err)
	}
	b.VersionAfter = time.Since(start)
	if version != OracleVersion {
		return b, ErrVersionMismatch(version)
	}
	if err := poll(ctx, interval, func() (bool, error) {
		h, err := c.Health(ctx)
		if err != nil {
			return false, nil
		}
		b.Health = h
		return h.Status == "healthy" && h.DatabaseHealthy && h.WorkersAlive >= workers, nil
	}); err != nil {
		return b, fmt.Errorf("oracle: not healthy with %d workers within %s (last %+v): %w", workers, timeout, b.Health, err)
	}
	b.HealthyAfter = time.Since(start)
	return b, nil
}

// poll calls f every interval until it reports done, f fails, or ctx ends.
func poll(ctx context.Context, interval time.Duration, f func() (bool, error)) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		done, err := f()
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Bootstrap logs in as the seeded superadmin and keeps the returned text as
// the bearer token, whatever it is, so the same code works under NO_AUTH
// (OR-6).
func Bootstrap(ctx context.Context, c *Client) error {
	tok, err := c.Login(ctx, AdminEmail, AdminPassword)
	if err != nil {
		return err
	}
	if tok == "" {
		return errors.New("oracle: login returned an empty token")
	}
	c.Token = tok
	return nil
}

// NewWorkspace creates a harness-owned workspace for one fixture run and
// checks that its queue is empty (OR-6/a). Pushing only into such a
// workspace keeps the destructive push away from anything else (OR-31).
// When the check fails, the workspace is deleted again, so an error means
// that the harness owns no new workspace.
func NewWorkspace(ctx context.Context, c *Client, id string) error {
	if len(id) > 50 {
		return fmt.Errorf("oracle: workspace id %q is longer than 50 characters", id)
	}
	if err := c.CreateWorkspace(ctx, id); err != nil {
		return err
	}
	n, err := c.QueueCount(ctx, id)
	if err == nil && n != 0 {
		err = fmt.Errorf("oracle: new workspace %s has %d queued jobs", id, n)
	}
	if err != nil {
		if derr := c.DeleteWorkspace(ctx, id); derr != nil {
			return errors.Join(err, derr)
		}
		return err
	}
	return nil
}

// ApplySetup creates a fixture's setup.yaml items after the push, because
// the push removes selected items absent from the tree (OR-31): resource
// types, then variables, then resources.
func ApplySetup(ctx context.Context, c *Client, workspace string, s *conformance.Setup) error {
	if s == nil {
		return nil
	}
	for _, t := range s.ResourceTypes {
		if err := c.CreateResourceType(ctx, workspace, t.Name, t.Schema, t.Description); err != nil {
			return err
		}
	}
	for _, v := range s.Variables {
		if err := c.CreateVariable(ctx, workspace, v.Path, v.Value, v.IsSecret, v.Description); err != nil {
			return err
		}
	}
	for _, r := range s.Resources {
		if err := c.CreateResource(ctx, workspace, r.Path, r.ResourceType, r.Value, r.Description); err != nil {
			return err
		}
	}
	return nil
}

// Deployment is what the gate saw: each item's status after its lock job,
// and each flow's deployed module ids.
type Deployment struct {
	Scripts map[string]DeploymentStatus `json:"scripts"`
	Flows   map[string]DeploymentStatus `json:"flows"`
	Modules map[string][]string         `json:"modules"`
}

// Gate waits for the deployment of everything the push created (OR-9). For
// each script and flow: when the status has a job_id, the lock job is
// polled until it completed and must have succeeded; the status is then
// read again and its lock_error_logs must be empty, and a script's lock must
// be present. A flow's job_id persists after its lock job, so the gate
// never waits for it to clear. A failure here is a fixture error, not a
// semantic difference.
func Gate(ctx context.Context, c *Client, workspace string, interval, timeout time.Duration) (Deployment, error) {
	d := Deployment{Scripts: map[string]DeploymentStatus{}, Flows: map[string]DeploymentStatus{}, Modules: map[string][]string{}}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	scripts, err := c.ListScripts(ctx, workspace)
	if err != nil {
		return d, err
	}
	for _, s := range scripts {
		st, err := settle(ctx, c, workspace, interval, func() (DeploymentStatus, error) {
			return c.ScriptDeploymentStatus(ctx, workspace, s.Hash)
		})
		if err != nil {
			return d, fmt.Errorf("oracle: script %s: %w", s.Path, err)
		}
		if st.Lock == nil {
			return d, fmt.Errorf("oracle: script %s has no lock after deployment", s.Path)
		}
		d.Scripts[s.Path] = st
	}
	flows, err := c.ListFlows(ctx, workspace)
	if err != nil {
		return d, err
	}
	for _, p := range flows {
		st, err := settle(ctx, c, workspace, interval, func() (DeploymentStatus, error) {
			return c.FlowDeploymentStatus(ctx, workspace, p)
		})
		if err != nil {
			return d, fmt.Errorf("oracle: flow %s: %w", p, err)
		}
		d.Flows[p] = st
		raw, err := c.GetFlow(ctx, workspace, p)
		if err != nil {
			return d, err
		}
		var f struct {
			Value struct {
				Modules []struct {
					ID string `json:"id"`
				} `json:"modules"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return d, fmt.Errorf("oracle: flow %s: %w", p, err)
		}
		ids := make([]string, len(f.Value.Modules))
		for i, m := range f.Value.Modules {
			ids[i] = m.ID
		}
		d.Modules[p] = ids
	}
	return d, nil
}

// settle reads a deployment status, waits for its lock job when it names
// one, reads it again, and checks lock_error_logs.
func settle(ctx context.Context, c *Client, workspace string, interval time.Duration, read func() (DeploymentStatus, error)) (DeploymentStatus, error) {
	st, err := read()
	if err != nil {
		return st, err
	}
	if st.JobID != nil {
		if err := WaitJob(ctx, c, workspace, *st.JobID, interval, true); err != nil {
			return st, fmt.Errorf("lock job: %w", err)
		}
		if st, err = read(); err != nil {
			return st, err
		}
	}
	if st.LockErrorLogs != nil && *st.LockErrorLogs != "" {
		return st, fmt.Errorf("lock_error_logs: %s", *st.LockErrorLogs)
	}
	return st, nil
}

// WaitJob polls get_result_maybe until the job completed (OR-10). With
// requireSuccess, a job that completed without success is an error.
func WaitJob(ctx context.Context, c *Client, workspace, id string, interval time.Duration, requireSuccess bool) error {
	var last ResultMaybe
	err := poll(ctx, interval, func() (bool, error) {
		r, err := c.ResultMaybe(ctx, workspace, id)
		if err != nil {
			return false, err
		}
		last = r
		return r.Completed, nil
	})
	if err != nil {
		return err
	}
	if requireSuccess && (last.Success == nil || !*last.Success) {
		return fmt.Errorf("job %s did not succeed: %s", id, last.Result)
	}
	return nil
}

// CheckDeployedModules compares each fixture flow's top-level module ids
// with the deployed flow's (OR-8/b). The deployed list may end with one
// generated <id>-v module (OR-V7).
func CheckDeployedModules(tree *SyncTree, d Deployment) error {
	for path, f := range tree.Flows {
		got, ok := d.Modules[path]
		if !ok {
			return fmt.Errorf("oracle: flow %s of the sync tree was not deployed", path)
		}
		want := make([]FlowStatusModule, 0, len(f.Value.Modules))
		for _, m := range f.Value.Modules {
			want = append(want, FlowStatusModule{ID: m.ID})
		}
		entries := make([]FlowStatusModule, len(got))
		for i, id := range got {
			entries[i] = FlowStatusModule{ID: id}
		}
		user := got
		if len(entries) >= 2 && generated(entries, bodyDef(f.Value.Modules, nil)) {
			user = got[:len(got)-1]
		}
		if len(user) != len(want) {
			return fmt.Errorf("oracle: flow %s deployed modules %q, the sync tree has %d", path, got, len(want))
		}
		for i := range want {
			if user[i] != want[i].ID {
				return fmt.Errorf("oracle: flow %s deployed modules %q, the sync tree's module %d is %q", path, got, i, want[i].ID)
			}
		}
	}
	return nil
}
