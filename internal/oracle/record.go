package oracle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// Session records fixtures on one oracle (OR-18): one compose stack per
// session, bootstrapped once, a fresh workspace per fixture, and the runs
// recorded in sequence. It always tears the stack down.
type Session struct {
	Docker Docker
	CLI    CLI
	// Workers is the number of worker containers, 1 or 2 (OR-5).
	Workers int
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// DataRoot is the directory the disk policy measures; empty means
	// DockerDataRoot.
	DataRoot string
	// SinkListen is the effect sink's listen address; empty means
	// 0.0.0.0:0, reachable from the containers through the host gateway.
	SinkListen string
	// RunTimeout bounds one run: zero means 5 minutes, and less than 60 s
	// is raised to 60 s (OR-10). A run that times out is canceled.
	RunTimeout time.Duration
	// Repeat runs every run that many times and requires equal
	// observations (OR-14/a). Zero means 1.
	Repeat int
	// RegenerateMetadata runs folder add-missing and generate-metadata in
	// each fixture's sync tree before the push, in place, so a CI step can
	// diff the tree (the drift gate of OR-8). A generated fixture's tree
	// (conformance.LiftExprFile) gets its metadata this way whatever the
	// setting, since none is committed for it.
	RegenerateMetadata bool
	// Check fails a run that has no committed recording or whose
	// observation differs from it, and never writes into a fixture.
	Check bool
	// OutDir, when set, receives new and changed recordings at
	// <OutDir>/<recording directory relative to RepoRoot>/<run>.json, for
	// a flow fixture <OutDir>/<fixture path>/recorded/<run>.json, instead
	// of the recording directory.
	OutDir string
	// RawDir, when set, receives the raw API captures of every run.
	RawDir string
	// RepoRoot is the directory fixture paths are reported relative to.
	RepoRoot string
	// Harness describes the harness build, for provenance.
	Harness HarnessInfo

	// Test seams: the oracle's URL, the poll interval, the disk
	// measurement and the settle delay of OR-30 step 6.
	baseURL       string
	pollInterval  time.Duration
	measure       FreeSpace
	settle        time.Duration
	minRunTimeout time.Duration
	runWatch      time.Duration
}

func (s *Session) base() string {
	if s.baseURL != "" {
		return s.baseURL
	}
	return BaseURL
}

// Outcomes of a run.
const (
	OutcomeUnchanged      = "unchanged"
	OutcomeNew            = "new"
	OutcomeChanged        = "changed"
	OutcomeUnstable       = "unstable"
	OutcomeExpectMismatch = "expect_mismatch"
	OutcomeFailed         = "failed"
	OutcomeRefused        = "refused"
)

// Report is the outcome of a session.
type Report struct {
	Session SessionReport `json:"session"`
	Runs    []RunReport   `json:"runs"`
}

// SessionReport holds the session's measurements (OR-4/a, OR-30, Q-OR-2).
type SessionReport struct {
	Arch            string               `json:"arch"`
	FreeBeforeBytes uint64               `json:"free_before_bytes"`
	PullNeedBytes   uint64               `json:"pull_need_bytes"`
	Pulled          []string             `json:"pulled"`
	FreeAfterBytes  uint64               `json:"free_after_bytes"`
	Images          map[string]ImageInfo `json:"images"`
	// VersionAfterMS and HealthyAfterMS count from compose up (OR-4/a).
	VersionAfterMS int64                 `json:"version_after_ms"`
	HealthyAfterMS int64                 `json:"healthy_after_ms"`
	CLIVersion     string                `json:"cli_version"`
	MemoryPeak     map[string]int64      `json:"memory_peak_bytes"`
	Deployments    map[string]Deployment `json:"deployments"`
	Teardown       []string              `json:"teardown_leftovers"`
}

// RunReport is the outcome of one run.
type RunReport struct {
	Fixture     string   `json:"fixture"`
	Run         string   `json:"run"`
	Outcome     string   `json:"outcome"`
	Differences []string `json:"differences,omitempty"`
	Error       string   `json:"error,omitempty"`
	Wrote       string   `json:"wrote,omitempty"`
}

// Failed reports whether the run fails the session: always for a failure,
// refusal, instability or expectation mismatch, and in check mode also for
// a new or changed recording (OR-19).
func (r RunReport) Failed(check bool) bool {
	switch r.Outcome {
	case OutcomeUnchanged:
		return false
	case OutcomeNew, OutcomeChanged:
		return check
	}
	return true
}

func (s *Session) logf(format string, a ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", a...)
	}
}

// Run records fixtures. A run-level problem is reported in its RunReport;
// an error means the session itself could not proceed.
func (s *Session) Run(ctx context.Context, fixtures []*conformance.Fixture) (*Report, error) {
	if s.Workers == 0 {
		s.Workers = 1
	}
	if s.pollInterval == 0 {
		s.pollInterval = 200 * time.Millisecond
	}
	if s.settle == 0 {
		s.settle = 30 * time.Second
	}
	if s.runWatch == 0 {
		s.runWatch = 10 * time.Second
	}
	if s.minRunTimeout == 0 {
		s.minRunTimeout = 60 * time.Second // OR-10: never less than 60 s
	}
	switch {
	case s.RunTimeout == 0:
		s.RunTimeout = 5 * time.Minute
	case s.RunTimeout < s.minRunTimeout:
		s.RunTimeout = s.minRunTimeout
	}
	rep := &Report{Session: SessionReport{Images: map[string]ImageInfo{}, MemoryPeak: map[string]int64{}, Deployments: map[string]Deployment{}}}

	left, err := Leftovers(ctx, s.Docker)
	if err != nil {
		return rep, err
	}
	if len(left) > 0 {
		return rep, fmt.Errorf("oracle: an earlier %s session was not torn down (%s); run docker compose -p %s down -v", ProjectName, strings.Join(left, ", "), ProjectName)
	}
	if err := s.pull(ctx, rep); err != nil {
		return rep, err
	}
	// Whether or not anything was pulled, the oracle starts only with the
	// reserve free (OR-30 steps 6 and 7); images that were already present
	// stay.
	free, err := s.measure()
	if err != nil {
		return rep, err
	}
	if free < Reserve {
		return rep, ErrDiskFloor("start", free, Reserve)
	}

	tmp, err := os.MkdirTemp("", "wltest-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(tmp)

	var sink Sink
	listen := s.SinkListen
	if listen == "" {
		listen = "0.0.0.0:0"
	}
	port, err := sink.Listen(listen)
	if err != nil {
		return rep, err
	}
	defer sink.Shutdown()
	sinkURL := fmt.Sprintf("http://host.docker.internal:%d", port)

	file, err := WriteComposeFile(tmp)
	if err != nil {
		return rep, err
	}
	compose := Compose{Docker: s.Docker, File: file, Workers: s.Workers, Env: []string{"WL_EFFECT_SINK=" + sinkURL}}
	s.logf("starting the oracle (%d worker container(s))", s.Workers)
	// The watchdog covers the start as well as the session (OR-30 step 7);
	// teardown runs whatever happened.
	runErr := Watch(ctx, "run", s.measure, s.runWatch, func(ctx context.Context) error {
		upAt := time.Now()
		if err := compose.Up(ctx); err != nil {
			return err
		}
		return s.session(ctx, rep, fixtures, tmp, sinkURL, &sink, upAt)
	})
	if err := s.teardown(compose, rep); err != nil && runErr == nil {
		runErr = err
	}
	return rep, runErr
}

// pull pulls the missing images under the disk policy (OR-30) and checks
// both images' identity (OR-1).
func (s *Session) pull(ctx context.Context, rep *Report) error {
	if s.measure == nil {
		root := s.DataRoot
		if root == "" {
			r, err := DockerDataRoot(ctx, s.Docker)
			if err != nil {
				return err
			}
			root = r
		}
		s.measure = func() (uint64, error) { return freeBytes(root) }
	}
	arch, err := DockerArch(ctx, s.Docker)
	if err != nil {
		return err
	}
	rep.Session.Arch = arch
	var missing []string
	for _, img := range Images {
		_, ok, err := InspectImage(ctx, s.Docker, img.Ref)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, img.Ref)
		}
	}
	free, err := s.measure()
	if err != nil {
		return err
	}
	rep.Session.FreeBeforeBytes = free
	rep.Session.PullNeedBytes = PullNeed(missing, arch)
	if len(missing) > 0 {
		if err := CheckPull(free, rep.Session.PullNeedBytes); err != nil {
			return err
		}
		for _, ref := range missing {
			s.logf("pulling %s", ref)
			if err := Watch(ctx, "pull", s.measure, 2*time.Second, func(ctx context.Context) error {
				_, err := s.Docker.Run(ctx, nil, "pull", ref)
				return err
			}); err != nil {
				return err
			}
			rep.Session.Pulled = append(rep.Session.Pulled, ref)
		}
		after, err := SettledFree(ctx, s.measure, s.settle)
		if err != nil {
			return err
		}
		rep.Session.FreeAfterBytes = after
		if after < Reserve {
			for _, ref := range rep.Session.Pulled {
				_, _ = s.Docker.Run(ctx, nil, "image", "rm", ref)
			}
			return ErrDiskAfterPull(after, Reserve, rep.Session.Pulled)
		}
	} else {
		rep.Session.FreeAfterBytes = free
	}
	for _, img := range Images {
		info, ok, err := InspectImage(ctx, s.Docker, img.Ref)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("oracle: image %s is missing after the pull", img.Ref)
		}
		if err := img.CheckIdentity(info); err != nil {
			return err
		}
		rep.Session.Images[img.Service] = info
	}
	return nil
}

// teardown removes the stack and checks that nothing is left (OR-18/a).
func (s *Session) teardown(c Compose, rep *Report) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, svc := range []string{"windmill", "db", "worker"} {
		if id, err := c.ContainerID(ctx, svc); err == nil {
			if peak, ok := MemoryPeak(id); ok {
				rep.Session.MemoryPeak[svc] = peak
			}
		}
	}
	s.logf("tearing the oracle down")
	if err := c.Down(ctx); err != nil {
		return err
	}
	left, err := Leftovers(ctx, s.Docker)
	if err != nil {
		return err
	}
	rep.Session.Teardown = left
	if len(left) > 0 {
		return fmt.Errorf("oracle: teardown left %s", strings.Join(left, ", "))
	}
	return nil
}

func (s *Session) session(ctx context.Context, rep *Report, fixtures []*conformance.Fixture, tmp, sinkURL string, sink *Sink, upAt time.Time) error {
	c := &Client{BaseURL: s.base()}
	sinceUp := time.Since(upAt)
	boot, err := WaitReady(ctx, c, s.Workers, time.Second, 180*time.Second)
	rep.Session.VersionAfterMS = (sinceUp + boot.VersionAfter).Milliseconds()
	rep.Session.HealthyAfterMS = (sinceUp + boot.HealthyAfter).Milliseconds()
	if err != nil {
		return err
	}
	if err := Bootstrap(ctx, c); err != nil {
		return err
	}
	cli := s.CLI
	cli.Home = filepath.Join(tmp, "cli-home")
	cli.ConfigDir = filepath.Join(tmp, "cli-config")
	for _, d := range []string{cli.Home, cli.ConfigDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	cliVersion, err := cli.Version(ctx)
	rep.Session.CLIVersion = cliVersion
	if err != nil {
		return err
	}
	short := randomHex(3)
	env := map[string]string{"WL_EFFECT_SINK": sinkURL}
	for k, v := range OracleEnv {
		env[k] = v
	}
	base := Provenance{
		Oracle: OracleProvenance{
			Version:       OracleVersion,
			Image:         WindmillImage,
			ImageID:       rep.Session.Images["windmill"].ID,
			ImageRevision: rep.Session.Images["windmill"].Labels["org.opencontainers.image.revision"],
			Postgres:      PostgresImage,
			PostgresID:    rep.Session.Images["db"].ID,
			CLI:           CLIPackage,
			CLIVersion:    cliVersion,
		},
		Harness: s.Harness,
		Host:    hostInfo(rep.Session.Arch),
		Workers: s.Workers,
		Env:     env,
	}
	for i, f := range fixtures {
		name := s.rel(f.Dir)
		if need := f.Oracle.NeedWorkers(); need > s.Workers {
			for _, r := range f.Runs {
				rep.Runs = append(rep.Runs, RunReport{Fixture: name, Run: r.Name, Outcome: OutcomeRefused, Error: ErrRequiresWorkers(need, s.Workers).Error()})
			}
			continue
		}
		// A run whose committed recording was made on another oracle or
		// with another loader is refused before anything is deployed or
		// run, whatever its observation says (OR-V5/b).
		var runs []conformance.Run
		for _, r := range f.Runs {
			if r.Recorded != nil {
				if err := CheckRecordingIdentity(r.Recorded.Provenance); err != nil {
					rep.Runs = append(rep.Runs, RunReport{Fixture: name, Run: r.Name, Outcome: OutcomeRefused, Error: err.Error()})
					continue
				}
			}
			runs = append(runs, r)
		}
		if len(runs) == 0 {
			continue
		}
		ws := fmt.Sprintf("wl-%s-%d", short, i+1)
		s.logf("fixture %s: workspace %s", name, ws)
		created, err := NewWorkspace(ctx, c, ws)
		var tree *SyncTree
		if err == nil {
			var dep Deployment
			tree, dep, err = s.deploy(ctx, c, cli, f, ws, tmp)
			rep.Session.Deployments[name] = dep
		}
		if err != nil {
			for _, r := range runs {
				rep.Runs = append(rep.Runs, RunReport{Fixture: name, Run: r.Name, Outcome: OutcomeFailed, Error: err.Error()})
			}
		} else {
			for _, r := range runs {
				rep.Runs = append(rep.Runs, s.record(ctx, c, ws, f, r, tree, sink, base))
			}
		}
		if !created {
			continue
		}
		// CE allows two workspaces besides admins, so the next fixture's
		// fresh workspace needs this one gone, whatever happened in it. A
		// workspace that cannot be deleted ends the session: every later
		// fixture would fail. The deletion has a context of its own, so a
		// canceled session still cleans up and reports the cancellation.
		del, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = c.DeleteWorkspace(del, ws)
		cancel()
		if err != nil {
			return fmt.Errorf("oracle: deleting workspace %s: %w", ws, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// rel returns a fixture directory relative to RepoRoot, both made
// absolute first, in slash form.
func (s *Session) rel(p string) string {
	if s.RepoRoot == "" {
		return filepath.ToSlash(p)
	}
	root, err1 := filepath.Abs(s.RepoRoot)
	abs, err2 := filepath.Abs(p)
	if err1 == nil && err2 == nil {
		if r, err := filepath.Rel(root, abs); err == nil {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(p)
}

// deploy optionally regenerates the fixture's metadata, pushes a copy of
// its sync tree with the CLI into the fixture's new workspace ws, applies
// its setup and passes the deployment gate.
func (s *Session) deploy(ctx context.Context, c *Client, cli CLI, f *conformance.Fixture, ws, tmp string) (*SyncTree, Deployment, error) {
	var dep Deployment
	t := Target{BaseURL: s.base(), Token: c.Token, Workspace: ws}
	if s.RegenerateMetadata || f.Generated {
		if err := cli.AddMissingFolders(ctx, f.SyncTree(), t); err != nil {
			return nil, dep, err
		}
		if err := cli.GenerateMetadata(ctx, f.SyncTree(), t); err != nil {
			return nil, dep, err
		}
	}
	tree, err := LoadSyncTree(f.SyncTree())
	if err != nil {
		return nil, dep, err
	}
	copyDir := filepath.Join(tmp, "push", ws)
	if err := CopyTree(f.SyncTree(), copyDir); err != nil {
		return nil, dep, err
	}
	if err := cli.Push(ctx, copyDir, t); err != nil {
		return nil, dep, err
	}
	if err := ApplySetup(ctx, c, ws, f.Setup); err != nil {
		return nil, dep, err
	}
	if dep, err = Gate(ctx, c, ws, s.pollInterval, 2*time.Minute); err != nil {
		return nil, dep, err
	}
	return tree, dep, CheckDeployedModules(tree, dep)
}

// record runs one fixture run Repeat times, compares the observations with
// each other, with the run's expect and with its committed recording, and
// writes the recording as the session's policy says.
func (s *Session) record(ctx context.Context, c *Client, ws string, f *conformance.Fixture, r conformance.Run, tree *SyncTree, sink *Sink, base Provenance) RunReport {
	out := RunReport{Fixture: s.rel(f.Dir), Run: r.Name}
	fail := func(outcome string, err error, diffs []conformance.Difference) RunReport {
		out.Outcome = outcome
		if err != nil {
			out.Error = err.Error()
		}
		for _, d := range diffs {
			out.Differences = append(out.Differences, d.String())
		}
		return out
	}
	repeat := max(s.Repeat, 1)
	// The run's expect may be a subset assertion; two oracle observations
	// are compared exactly, so an extra member or step is a change.
	expectOpts := f.Compare.Options()
	opts := expectOpts
	opts.Subset = false
	var first conformance.Observation
	var prov Provenance
	for i := 0; i < repeat; i++ {
		obs, p, err := s.execute(ctx, c, ws, f, r, tree, sink, base, i)
		if err != nil {
			return fail(OutcomeFailed, err, nil)
		}
		if i == 0 {
			first, prov = obs, p
			continue
		}
		diffs, err := conformance.Compare(obs, first, opts)
		if err != nil || len(diffs) > 0 {
			return fail(OutcomeUnstable, err, diffs)
		}
	}
	prov.Repeats = repeat
	if r.Expect != nil {
		ro := expectOpts
		ro.WithRecording = true
		diffs, err := conformance.Compare(first, *r.Expect, ro)
		if err != nil || len(diffs) > 0 {
			return fail(OutcomeExpectMismatch, err, diffs)
		}
	}
	out.Outcome = OutcomeNew
	if r.Recorded != nil {
		diffs, err := conformance.Compare(first, r.Recorded.Observation, opts)
		if err != nil {
			return fail(OutcomeFailed, err, nil)
		}
		out.Outcome = OutcomeUnchanged
		for _, d := range diffs {
			out.Outcome = OutcomeChanged
			out.Differences = append(out.Differences, d.String())
		}
	}
	if out.Outcome == OutcomeUnchanged {
		return out
	}
	dest := ""
	switch {
	case s.OutDir != "":
		rel := s.rel(f.RecordingDir())
		if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(filepath.FromSlash(rel)) {
			return fail(OutcomeFailed, fmt.Errorf("oracle: fixture %s is outside %s, so --out cannot place its recording", f.Dir, s.RepoRoot), nil)
		}
		dest = filepath.Join(s.OutDir, filepath.FromSlash(rel), r.Name+".json")
	case !s.Check:
		dest = f.RecordingPath(r.Name)
	}
	if dest != "" {
		pj, err := json.Marshal(prov)
		if err != nil {
			return fail(OutcomeFailed, err, nil)
		}
		if err := conformance.WriteRecording(dest, conformance.Recording{Observation: first, Provenance: pj}); err != nil {
			return fail(OutcomeFailed, err, nil)
		}
		out.Wrote = dest
	}
	return out
}

// execute submits one run, waits for it, walks it and normalises it.
func (s *Session) execute(ctx context.Context, c *Client, ws string, f *conformance.Fixture, r conformance.Run, tree *SyncTree, sink *Sink, base Provenance, rep int) (conformance.Observation, Provenance, error) {
	root := newUUID()
	sink.Open(root)
	defer sink.Close()
	submitted := time.Now()
	var id string
	var err error
	if r.IsScript() {
		id, err = c.RunScript(ctx, ws, r.Target, r.ArgsOrEmpty(), root)
	} else {
		id, err = c.RunFlow(ctx, ws, r.Target, r.ArgsOrEmpty(), root)
	}
	if err != nil {
		return conformance.Observation{}, base, err
	}
	if id != root {
		return conformance.Observation{}, base, fmt.Errorf("oracle: submitted root %s, Windmill returned %s", root, id)
	}
	if r.CancelAfterMS != nil {
		t := time.AfterFunc(time.Duration(*r.CancelAfterMS)*time.Millisecond, func() {
			_ = c.Cancel(ctx, ws, id, "wltest: cancel_after_ms")
		})
		defer t.Stop()
	}
	runCtx, cancel := context.WithTimeout(ctx, s.RunTimeout)
	err = WaitJob(runCtx, c, ws, id, s.pollInterval, false)
	cancel()
	if err != nil {
		// A run left going would hold the only worker and post its late
		// effects into later runs: cancel it and give it a moment to stop.
		stop, done := context.WithTimeout(context.Background(), 30*time.Second)
		if cerr := c.Cancel(stop, ws, id, "wltest: run timeout"); cerr == nil {
			_ = WaitJob(stop, c, ws, id, s.pollInterval, false)
		}
		done()
		return conformance.Observation{}, base, fmt.Errorf("oracle: run %s did not complete (canceled): %w", r.Name, err)
	}
	elapsed := time.Since(submitted)
	effects, foreign := sink.Close()

	capture := &Capture{}
	wc := *c
	wc.Capture = capture
	w, err := (&Walker{Client: &wc, Workspace: ws, Tree: tree}).Walk(ctx, id, r.Target, r.IsScript())
	if s.RawDir != "" {
		if werr := s.writeRaw(f, r, rep, ws, root, effects, capture); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		return conformance.Observation{}, base, err
	}
	obs, err := Observe(w, effects, f.Compare)
	if err != nil {
		return obs, base, err
	}
	p := base
	p.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	p.Timings = RunTimings{SubmitToCompleteMS: elapsed.Milliseconds(), Jobs: w.Timings}
	p.ForeignEffects = foreign
	return obs, p, nil
}

// RawCapture is a raw API capture of one run, kept to diagnose
// normalisation and to replay the walk in tests (OR-V5, OR-28/a).
type RawCapture struct {
	Fixture   string            `json:"fixture"`
	Run       string            `json:"run"`
	Repeat    int               `json:"repeat"`
	Workspace string            `json:"workspace"`
	Root      string            `json:"root"`
	Effects   []json.RawMessage `json:"effects"`
	Exchanges []Exchange        `json:"exchanges"`
}

func (s *Session) writeRaw(f *conformance.Fixture, r conformance.Run, rep int, ws, root string, effects []json.RawMessage, c *Capture) error {
	rc := RawCapture{Fixture: s.rel(f.Dir), Run: r.Name, Repeat: rep, Workspace: ws, Root: root, Effects: effects, Exchanges: c.Exchanges}
	if rc.Effects == nil {
		rc.Effects = []json.RawMessage{}
	}
	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return err
	}
	name := strings.ReplaceAll(s.rel(f.Dir), "/", "_") + "-" + r.Name + fmt.Sprintf("-%d.json", rep)
	if err := os.MkdirAll(s.RawDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.RawDir, name), append(data, '\n'), 0o644)
}

// Observe normalises a walk and the run's effects into an observation
// (OR-14, OR-15), then applies the fixture's stack and mask paths.
func Observe(w *Walk, effects []json.RawMessage, spec conformance.CompareSpec) (conformance.Observation, error) {
	n := &conformance.Normaliser{IDs: w.IDs, RuntimeErrors: w.RuntimeErrors}
	var err error
	o := conformance.Observation{Status: w.Status}
	if o.Result, err = n.Value(w.Result); err != nil {
		return o, err
	}
	if len(w.Sites) > 0 {
		o.Steps = make(map[conformance.SitePath]conformance.StepObservation, len(w.Sites))
	}
	for _, site := range w.Sites {
		st := site.Step
		if _, dup := o.Steps[site.Path]; dup {
			return o, fmt.Errorf("oracle: site %s walked twice", site.Path)
		}
		if st.Args, err = n.Value(st.Args); err != nil {
			return o, err
		}
		if st.Result, err = n.Value(st.Result); err != nil {
			return o, err
		}
		o.Steps[site.Path] = st
	}
	for _, e := range effects {
		v, err := n.Value(e)
		if err != nil {
			return o, err
		}
		o.Effects = append(o.Effects, v)
	}
	return conformance.MaskPaths(o, spec.Stack, spec.Mask)
}

// Provenance is a recording's provenance, kept apart from its observation
// and never compared (OR-15, OR-V5).
type Provenance struct {
	Oracle         OracleProvenance  `json:"oracle"`
	Harness        HarnessInfo       `json:"harness"`
	Host           HostInfo          `json:"host"`
	RecordedAt     string            `json:"recorded_at"`
	Workers        int               `json:"workers"`
	Env            map[string]string `json:"env"`
	Repeats        int               `json:"repeats"`
	Timings        RunTimings        `json:"timings"`
	ForeignEffects int               `json:"foreign_effects,omitempty"`
}

// CheckRecordingIdentity requires a recording's provenance to name the
// pinned reference: oracle.version CE v1.767.0, oracle.image the pinned
// image and digest, oracle.cli windmill-cli@1.767.0 (OR-15, OR-V5/b). A
// missing, malformed or different identity is refused with
// recording_identity_mismatch. Dates, timings, the host and the other
// provenance fields are never checked.
func CheckRecordingIdentity(provenance json.RawMessage) error {
	var p struct {
		Oracle *struct {
			Version *string `json:"version"`
			Image   *string `json:"image"`
			CLI     *string `json:"cli"`
		} `json:"oracle"`
	}
	if err := json.Unmarshal(provenance, &p); err != nil || p.Oracle == nil {
		return refusal("recording_identity_mismatch", "field", jsonString("oracle"), "got", jsonString(string(provenance)))
	}
	for _, f := range []struct {
		name string
		got  *string
		want string
	}{
		{"oracle.version", p.Oracle.Version, OracleVersion},
		{"oracle.image", p.Oracle.Image, WindmillImage},
		{"oracle.cli", p.Oracle.CLI, CLIPackage},
	} {
		got := "(absent)"
		if f.got != nil {
			got = *f.got
		}
		if got != f.want {
			return refusal("recording_identity_mismatch", "field", jsonString(f.name), "got", jsonString(got), "want", jsonString(f.want))
		}
	}
	return nil
}

// OracleProvenance identifies the oracle and the loader (OR-1, OR-2, OR-8).
type OracleProvenance struct {
	Version       string `json:"version"`
	Image         string `json:"image"`
	ImageID       string `json:"image_id"`
	ImageRevision string `json:"image_revision"`
	Postgres      string `json:"postgres_image"`
	PostgresID    string `json:"postgres_image_id"`
	CLI           string `json:"cli"`
	CLIVersion    string `json:"cli_version_output"`
}

// HarnessInfo identifies the harness build.
type HarnessInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Revision string `json:"revision,omitempty"`
	Modified bool   `json:"modified,omitempty"`
	Go       string `json:"go"`
}

// HostInfo describes where the session ran.
type HostInfo struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	DockerArch string `json:"docker_arch"`
	CI         string `json:"ci,omitempty"`
	RunURL     string `json:"run_url,omitempty"`
}

// RunTimings are a run's raw timings (OR-17): never compared.
type RunTimings struct {
	SubmitToCompleteMS int64                `json:"submit_to_complete_ms"`
	Jobs               map[string]JobTiming `json:"jobs"`
}

func hostInfo(dockerArch string) HostInfo {
	h := HostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, DockerArch: dockerArch}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		h.CI = "github-actions"
		if srv, repo, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"); srv != "" && repo != "" && id != "" {
			h.RunURL = srv + "/" + repo + "/actions/runs/" + id
		}
	}
	return h
}

// newUUID returns a random version 4 UUID, used to pre-assign a run's root
// job id (OR-10).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ErrNoFixtures reports that a session was given no fixtures.
var ErrNoFixtures = errors.New("oracle: no fixtures")
