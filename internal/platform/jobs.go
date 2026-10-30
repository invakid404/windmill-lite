package platform

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// JobKind is the kind of a job the job registry knows of: a flow job (a
// flow run, or a child flow that an inline body runs as) or a script job
// (a flow step's job, or a script run). Only a flow job holds flow user
// state (WA-14).
type JobKind string

const (
	JobFlow   JobKind = "flow"
	JobScript JobKind = "script"
)

// Job is what the job registry knows of one job (D3).
type Job struct {
	// ID is the job's id, a windmill-lite UUID.
	ID   string
	Kind JobKind
	// Root is the job's effective root (WA-V1): what getRootJobId answers
	// for it, and the flow job whose user state it shares (WA-14). A flow
	// run that nothing dispatched is its own root.
	Root string
	// Path is the job's runnable path (WA-6), "" when it is not known.
	Path string
	// Env is a script job's job variables as its step received them,
	// from which a resource read made with its job_id resolves $WM_*
	// strings (APIEnv); nil for a flow job.
	Env map[string]string
}

// Jobs is the job registry of ADR 0007 (D3): the jobs a runtime has
// started, which answers job lookups for windmill-lite ids from the runs
// instead of a job table. The engine registers each flow run, and the
// facade each invocation, a script job, while it runs. A job stays known
// while its root runs, so a job of a live run can be looked up after it
// ended; once the root and every job of its tree have ended, the tree is
// forgotten, and its ids are unknown again.
type Jobs interface {
	// Begin registers a job that has started and returns the function that
	// ends it, which may be called more than once. A script job's parent
	// flow, innermost flow scope and root, which its job variables name
	// (WM_FLOW_JOB_ID, WM_ROOT_FLOW_JOB_ID, WM_ROOT_JOB_ID), become known
	// as flow jobs of its root, live while the root is.
	Begin(j Job) (end func())
	// Job returns what the registry knows of the job id names: the job,
	// whether it is still live, and whether it is known at all.
	Job(id string) (j Job, live, ok bool)
}

// FlowUserState is the flow user state service of ADR 0007 (WA-14): a
// key/value map per flow job, which the stock helpers address by their
// effective root. The facade and the engine only read or write the map of
// a live flow job (Services.FlowUserState, Services.SetFlowUserState), so
// a service need not check that itself.
type FlowUserState interface {
	// UserState returns the value stored under key for the flow job, as
	// the JSON text a read gives back, and false when there is none.
	UserState(ctx context.Context, job, key string) (json.RawMessage, bool, error)
	// SetUserState stores v, the JSON text a read is to give back, under
	// key for the flow job.
	SetUserState(ctx context.Context, job, key string, v json.RawMessage) error
}

// Registry is the local default of the job registry: the jobs of one
// runtime, in memory. It is safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	jobs  map[string]*regJob
	trees map[string]map[string]bool // root → the ids of its jobs
}

// regJob is one job of the registry.
type regJob struct {
	job Job
	// live counts the registrations not yet ended; derived marks a flow
	// job known only from a script job's variables, which is live while
	// its root is.
	live    int
	derived bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{jobs: map[string]*regJob{}, trees: map[string]map[string]bool{}}
}

// Begin registers j. A registration of an id the registry derived makes
// it explicit; a second registration of a live id counts, and the job
// ends when every registration has ended.
func (r *Registry) Begin(j Job) (end func()) {
	if j.ID == "" {
		return func() {}
	}
	if j.Root == "" {
		j.Root = j.ID
	}
	r.mu.Lock()
	e := r.jobs[j.ID]
	switch {
	case e == nil:
		e = &regJob{job: j}
		r.jobs[j.ID] = e
		r.addToTree(j.Root, j.ID)
	case e.derived:
		r.removeFromTree(e.job.Root, j.ID)
		e.job, e.derived = j, false
		r.addToTree(j.Root, j.ID)
	}
	e.live++
	if j.Kind == JobScript {
		r.derive(j.Env["WM_FLOW_JOB_ID"], j.Root, j.Env["WM_FLOW_PATH"])
		r.derive(j.Env["WM_ROOT_FLOW_JOB_ID"], j.Root, "")
		r.derive(j.Root, j.Root, "")
	}
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { r.end(e) }) }
}

// derive makes a flow job of root known from a script job's variables,
// unless it is known already.
func (r *Registry) derive(id, root, path string) {
	if id == "" || r.jobs[id] != nil {
		return
	}
	r.jobs[id] = &regJob{job: Job{ID: id, Kind: JobFlow, Root: root, Path: path}, derived: true}
	r.addToTree(root, id)
}

func (r *Registry) addToTree(root, id string) {
	t := r.trees[root]
	if t == nil {
		t = map[string]bool{}
		r.trees[root] = t
	}
	t[id] = true
}

func (r *Registry) removeFromTree(root, id string) {
	if t := r.trees[root]; t != nil {
		delete(t, id)
		if len(t) == 0 {
			delete(r.trees, root)
		}
	}
}

// end ends one registration of e, and forgets e's tree once its root is
// no longer live.
func (r *Registry) end(e *regJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.live > 0 {
		e.live--
	}
	if !r.rootLive(e.job.Root) {
		r.sweep(e.job.Root)
	}
}

// rootLive reports whether the root is a registered job that is live.
func (r *Registry) rootLive(root string) bool {
	e := r.jobs[root]
	return e != nil && !e.derived && e.live > 0
}

// sweep forgets every job of root's tree that has ended: once the root
// has ended, nothing of its run can look them up. A job still live stays
// until it ends, and the derived flow jobs stay with it, so that it can
// still look up the scopes it runs in.
func (r *Registry) sweep(root string) {
	tree := r.trees[root]
	live := false
	for id := range tree {
		if e := r.jobs[id]; e != nil && !e.derived && e.live > 0 {
			live = true
			break
		}
	}
	for id := range tree {
		e := r.jobs[id]
		if e != nil && (e.live > 0 || e.derived && live) {
			continue
		}
		delete(r.jobs, id)
		delete(tree, id)
	}
	if len(tree) == 0 {
		delete(r.trees, root)
	}
}

// Job returns the job id names.
func (r *Registry) Job(id string) (Job, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.jobs[id]
	if e == nil {
		return Job{}, false, false
	}
	live := e.live > 0
	if e.derived {
		live = r.rootLive(e.job.Root)
	}
	return e.job, live, true
}

// UserStates is the local default of the flow user state service: the
// maps of the flow jobs that a job registry, whichever Jobs service the
// Services hold, reports live, in memory. Each map is kept with its job's
// effective root, read from the registry when the map is first written,
// and forgotten when that root's registration through the Services ends
// and the registry no longer reports it live (WA-14's design note: a
// windmill-lite lifetime policy, faithful for every access the stock
// helpers make during the run). So that it stays bounded with a Jobs
// service that knows runs registered elsewhere, it also drops, each time
// it has doubled since the last sweep, the maps of the jobs the registry
// no longer reports live, which no read could see. It is safe for
// concurrent use.
type UserStates struct {
	jobs Jobs

	mu    sync.Mutex
	maps  map[string]*userMap
	sweep int // the number of maps at which the next sweep runs
}

// userMap is one flow job's user state.
type userMap struct {
	root string
	vals map[string]json.RawMessage
}

// minSweep is the number of maps below which UserStates never sweeps.
const minSweep = 64

// NewUserStates returns the user state of the flow jobs that jobs knows.
func NewUserStates(jobs Jobs) *UserStates {
	return &UserStates{jobs: jobs, maps: map[string]*userMap{}, sweep: minSweep}
}

// UserState returns the value stored under key for the flow job.
func (u *UserStates) UserState(_ context.Context, job, key string) (json.RawMessage, bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.maps[job]
	if m == nil {
		return nil, false, nil
	}
	v, ok := m.vals[key]
	return v, ok, nil
}

// SetUserState stores v under key for the flow job, which must be one the
// job registry knows: a map for a job it does not know of would have no
// root to be forgotten with.
func (u *UserStates) SetUserState(_ context.Context, job, key string, v json.RawMessage) error {
	j, _, ok := u.jobs.Job(job)
	if !ok {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.maps[job]
	if m == nil {
		if len(u.maps) >= u.sweep {
			u.dropDead()
		}
		m = &userMap{root: cmp.Or(j.Root, job), vals: map[string]json.RawMessage{}}
		u.maps[job] = m
	}
	m.vals[key] = v
	return nil
}

// dropDead drops the maps of the jobs the registry no longer reports live,
// and sets the next sweep at twice the maps that remain. Called with mu
// held.
func (u *UserStates) dropDead() {
	for id := range u.maps {
		if _, live, ok := u.jobs.Job(id); !ok || !live {
			delete(u.maps, id)
		}
	}
	u.sweep = max(minSweep, 2*len(u.maps))
}

// forgetRoot drops the maps of root's jobs, once the registry no longer
// reports root live.
func (u *UserStates) forgetRoot(root string) {
	if _, live, ok := u.jobs.Job(root); ok && live {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, m := range u.maps {
		if m.root == root {
			delete(u.maps, id)
		}
	}
}

// size is the number of maps held.
func (u *UserStates) size() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.maps)
}

// servicesJobs is the job registry as the Services hand it to the engine
// and the facade: their Jobs service, whose registrations of a root also
// tell the default user state service when the root has ended, so that it
// forgets the root's maps.
type servicesJobs struct {
	Jobs
	states *UserStates // nil when the Services hold a FlowUserState of their own
}

func (s servicesJobs) Begin(j Job) func() {
	end := s.Jobs.Begin(j)
	if s.states == nil || j.ID == "" || cmp.Or(j.Root, j.ID) != j.ID {
		return end
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			end()
			s.states.forgetRoot(j.ID)
		})
	}
}

// noJobs is the job registry of a nil *Services: it keeps nothing.
type noJobs struct{}

func (noJobs) Begin(Job) func()             { return func() {} }
func (noJobs) Job(string) (Job, bool, bool) { return Job{}, false, false }

// ErrFlowJobNotFound is the API's answer to a flow user state write whose
// job is not a live flow job (WA-14): 404 `Not found: Flow job not found`.
var ErrFlowJobNotFound = &Error{Variant: "NotFound", Message: "Flow job not found"}

// RootJob answers getRootJobId (WA-14, WA-V1): the effective root of the
// job id names, and false when the registry does not know it.
func (s *Services) RootJob(id string) (string, bool) {
	j, _, ok := s.jobs().Job(id)
	if !ok {
		return "", false
	}
	return j.Root, true
}

// JobEnv returns the job variables of the script job id names, which a
// resource read made with its job_id resolves $WM_* strings from
// (APIEnv), and false when the registry knows no such script job.
func (s *Services) JobEnv(id string) (map[string]string, bool) {
	j, _, ok := s.jobs().Job(id)
	if !ok || j.Kind != JobScript || j.Env == nil {
		return nil, false
	}
	return j.Env, true
}

// ErrNoJobVariables marks a resource read whose job_id names a live job
// whose job variables the job registry does not keep: a flow job, whose
// stored fields windmill-lite cannot resolve $WM_* strings from.
var ErrNoJobVariables = errors.New("platform: the job registry keeps no job variables of a flow job")

// ReadJobEnv returns the reserved variables that a resource read made with
// a job id (job_id, WA-12) resolves $WM_* strings from: APIEnv of the job
// variables of the job id names, which must still be live and, when
// workspace is not "", of that workspace (its WM_WORKSPACE), as Windmill
// looks the job up among the queued and running jobs of the path's
// workspace. An unknown id, a job that has ended and a job of another
// workspace fail with the API's 404 `Not found: Job not found at name
// <id>`, written without Windmill's Rust location (D14); a live flow job
// fails with ErrNoJobVariables.
func (s *Services) ReadJobEnv(id, workspace string) (map[string]string, error) {
	j, live, ok := s.jobs().Job(id)
	switch {
	case !ok || !live || workspace != "" && j.Env["WM_WORKSPACE"] != "" && j.Env["WM_WORKSPACE"] != workspace:
		return nil, notFound("Job not found at name %s", id)
	case j.Kind != JobScript || j.Env == nil:
		return nil, ErrNoJobVariables
	}
	return APIEnv(j.Env), nil
}

// FlowUserState reads the flow user state at key of the job id names
// (WA-14, JE-20): the value stored there, or JSON null when there is none,
// when the job is not a flow job, and when it is no longer live, as the
// API answers once a root has left the queue.
func (s *Services) FlowUserState(ctx context.Context, id, key string) (json.RawMessage, error) {
	if !s.liveFlow(id) {
		return json.RawMessage("null"), nil
	}
	v, ok, err := s.userState().UserState(ctx, id, key)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return json.RawMessage("null"), nil
	}
	return v, nil
}

// SetFlowUserState writes body, a JSON text, at key of the job id names
// (WA-14). The job must be a live flow job, else the write fails with
// ErrFlowJobNotFound; a write to a run that has ended fails so too, since
// its map was discarded with it (windmill-lite's lifetime policy), as
// Windmill refuses it once the root's completion is committed (WA-14,
// After the run; WA-14/c). The value is held as a read gives it
// back, which the oracle recorded (fixture client-api/user-state, run
// order): the body as Windmill's backend JSON library parses and writes
// it (1e3 as 1000.0, 1.50 as 1.5), stored as the flow status column
// stores it, in jsonb's key order, and written out by the library again.
// A body that is not JSON, or that jsonb refuses, answers 400; the text is
// windmill-lite's.
func (s *Services) SetFlowUserState(ctx context.Context, id, key string, body []byte) error {
	v, err := userStateValue(body)
	if err != nil {
		return &Error{Variant: "BadRequest", Message: "windmill-lite: the flow user state value: " + err.Error()}
	}
	if !s.liveFlow(id) {
		return ErrFlowJobNotFound
	}
	return s.userState().SetUserState(ctx, id, key, v)
}

// userStateValue is a flow user state value as a read gives it back:
// parsed and written by the backend's JSON library, stored as jsonb, and
// written by the library again.
func userStateValue(body []byte) ([]byte, error) {
	n, err := parseJSON(body)
	if err != nil {
		return nil, err
	}
	stored, err := jsonval.FromProducer(n.write(nil)).Stored()
	if err != nil {
		return nil, err
	}
	if n, err = parseJSON(stored); err != nil {
		return nil, err
	}
	return n.write(nil), nil
}

func (s *Services) liveFlow(id string) bool {
	j, live, ok := s.jobs().Job(id)
	return ok && live && j.Kind == JobFlow
}
