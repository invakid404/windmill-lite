package platform

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
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
	// Run is the id of the run that started the job, whose View renders
	// its record; "" when no run of this runtime did. A run registers its
	// own flow job with its id as Run and its View.
	Run string
	// View renders the job's record for GET jobs_u/get/{id} ([WA-19]).
	// It is a reference to the run that holds the job, never a copy of
	// the run's results, and the registry drops it with the job's tree. A
	// job registered with no View is rendered by the View of the job that
	// its Run names.
	//
	// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
	View JobView
	// Host is the identity the job's run was given, the runtime's with the
	// run's own over it, with zero fields where neither set one (ADR 0007,
	// job identity). Its Workspace scopes the job: the facade finds it for
	// a caller only when the caller's run has the same Workspace
	// (Services.JobScope).
	// A job registered with a zero Host takes the Host of the job its Run
	// names, when the registry knows that job.
	Host Host
	// Gen numbers the registration that the registry holds for the job:
	// Registry gives each new registration of an id a number that no
	// earlier one had, so that a flow job's user state stays with the
	// registration that wrote it when its id ends and is registered again.
	// The registry sets it; 0 is a registry that numbers none.
	Gen uint64
}

// JobView renders the records of one run's jobs as GET jobs_u/get/{id}
// answers them ([WA-19]) while the run is live: a QueuedJob while a job
// runs and a CompletedJob once it has completed, with the fields that
// windmill-lite serves.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
type JobView interface {
	// JobRecord returns the record of the job that j registers, as the
	// JSON text the route answers: j is the registration that the read
	// checked, whose flow user state the record lists
	// (Services.FlowUserStatesOf), whatever the registry holds for its id
	// by then. It fails with a *NotServed when the run does not hold the
	// job, or holds it in a state whose record windmill-lite does not
	// reproduce.
	JobRecord(j Job) ([]byte, error)
	// JobArgs returns the stored arguments of the job id names, as GET
	// jobs_u/get_args/{id} answers them: jsonb's text output of the job's
	// args, or of a flow job's input. It fails as JobRecord does.
	JobArgs(id string) ([]byte, error)
	// JobPoll returns the state of the job id names for the result polls
	// ([WA-15], [WA-V2]): whether it has started, and once it has completed
	// whether it succeeded and its whole result in jsonb's text output,
	// which JobRecord replaces once it is too large. It serves the jobs
	// that JobRecord serves, and fails as JobRecord does for the others.
	//
	// [WA-15]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-15
	// [WA-V2]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-v2
	JobPoll(id string) (JobResult, error)
}

// NotServed is why windmill-lite does not answer GET jobs_u/get/{id} for a
// job it knows: the job's run has ended (deviation D22 of ADR 0001), or
// the job is in a state whose record windmill-lite does not reproduce. The
// facade answers it 501, so the 501 policy fails the step that asked.
type NotServed struct {
	Reason string
}

func (e *NotServed) Error() string { return e.Reason }

// ErrEndedRun is the NotServed of a job whose run has ended: the job
// registry keeps an ended job's id, kind, root and path for the root
// lookup, and drops the reference to its run (D22).
var ErrEndedRun = &NotServed{Reason: "the job's run has ended, and windmill-lite keeps no record of the jobs of a run that has ended"}

// Jobs is the job registry of ADR 0007 (D3): the jobs a runtime has
// started, which answers job lookups for windmill-lite ids from the runs
// instead of a job table. The engine registers each flow run, and the
// facade each invocation, a script job, while it runs. A job stays live
// while its root runs, so a job of a live run can be looked up after it
// ended. Once the root and every job of its tree have ended, a job is no
// longer live, but its root can still be looked up, as Windmill keeps a
// completed job's row until its job retention removes it ([WA-19]).
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
type Jobs interface {
	// Begin registers a job that has started and returns the function that
	// ends it, which may be called more than once. A script job's parent
	// flow, innermost flow scope and root, which its job variables name
	// (WM_FLOW_JOB_ID, WM_ROOT_FLOW_JOB_ID, WM_ROOT_JOB_ID), become known
	// as flow jobs of its root, live while the root is.
	Begin(j Job) (end func())
	// Job returns what the registry knows of the job id names: the job,
	// whether it is still live, and whether it is known at all. A job
	// that is known and no longer live may come without its Env.
	Job(id string) (j Job, live, ok bool)
}

// FlowUserState is the flow user state service of ADR 0007 (WA-14): a
// key/value map per flow job, which the stock helpers address by their
// effective root. The facade and the engine only read or write the map of
// a live flow job (Services.FlowUserState, Services.SetFlowUserState), so
// a service need not check that itself. A host's FlowUserState serves its
// engine's configured workspace only: the engine runs a run of another
// workspace only with the services' own store (Services.OwnsJobs).
type FlowUserState interface {
	// UserState returns the value stored under key for the flow job, as
	// the JSON text a read gives back, and false when there is none.
	UserState(ctx context.Context, job, key string) (json.RawMessage, bool, error)
	// SetUserState stores v, the JSON text a read is to give back, under
	// key for the flow job.
	SetUserState(ctx context.Context, job, key string, v json.RawMessage) error
}

// Registry is the local default of the job registry: the jobs of one
// runtime, in memory. Once a job's tree has ended, it keeps the job's id,
// kind, root and path, without its Env, among the latest endedJobs jobs
// whose trees ended; an older one is forgotten, and its id is unknown
// again. Windmill keeps a completed job's row for its job retention
// period, 30 days by default; windmill-lite bounds the memory by count
// instead. It is safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	jobs  map[string]*regJob
	trees map[string]map[string]bool // root → the ids of its jobs
	// ended holds the jobs of the trees that have ended, and order their
	// ids, oldest first, with the seq each was kept under; an entry of
	// order whose seq is not its id's in ended is stale (the id was
	// registered again) and is skipped.
	ended map[string]endedJob
	order []endedRef
	seq   uint64
	keep  int // the most ended jobs kept: endedJobs
	gen   uint64
}

// endedJobs is how many jobs of ended trees a Registry keeps known.
const endedJobs = 4096

// endedJob is a job of a tree that has ended, kept under seq: its kind,
// root and path, what the root lookup reads, and nothing else, so that an
// ended job costs no more than these strings, whatever Job holds while it
// is live.
type endedJob struct {
	kind                  JobKind
	root, path, workspace string
	seq                   uint64
}

type endedRef struct {
	id  string
	seq uint64
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
	return &Registry{jobs: map[string]*regJob{}, trees: map[string]map[string]bool{}, ended: map[string]endedJob{}, keep: endedJobs}
}

// Begin registers j. A registration of an id the registry derived makes
// it explicit; a second registration of a live id counts, and the job
// ends when every registration has ended.
func (r *Registry) Begin(j Job) (end func()) {
	end, _ = r.begin(j, false)
	return end
}

// BeginExclusive is Begin for a job whose id its run or host chose, such as
// a run's own id or a step's WM_JOB_ID: it registers j only when no job of
// another workspace holds the id, live or among the ended jobs it keeps, and
// reports whether it did. Runs of two workspaces so never share an entry of
// the registry (ADR 0007, a workspace per run).
func (r *Registry) BeginExclusive(j Job) (end func(), ok bool) {
	return r.begin(j, true)
}

// begin is Begin, and with exclusive BeginExclusive.
func (r *Registry) begin(j Job, exclusive bool) (end func(), ok bool) {
	if j.ID == "" {
		return func() {}, true
	}
	if j.Root == "" {
		j.Root = j.ID
	}
	r.mu.Lock()
	if j.Host == (Host{}) && j.Run != "" && j.Run != j.ID {
		// The jobs of a run are in its run's workspace, as its user's.
		if re := r.jobs[j.Run]; re != nil {
			j.Host = re.job.Host
		}
	}
	if exclusive && r.heldElsewhere(j) {
		r.mu.Unlock()
		return nil, false
	}
	delete(r.ended, j.ID)
	e := r.jobs[j.ID]
	switch {
	case e == nil:
		r.gen++
		j.Gen = r.gen
		e = &regJob{job: j}
		r.jobs[j.ID] = e
		r.addToTree(j.Root, j.ID)
	case e.derived:
		// The registration of a job known only from a script job: the same
		// job in the same workspace, else another one.
		j.Gen = e.job.Gen
		if !sameWorkspace(e.job.Host, j.Host) {
			r.gen++
			j.Gen = r.gen
		}
		r.removeFromTree(e.job.Root, j.ID)
		e.job, e.derived = j, false
		r.addToTree(j.Root, j.ID)
	case e.job.View == nil && j.View != nil && sameWorkspace(e.job.Host, j.Host):
		// A second registration that brings the run's view (a script
		// run registers its job before its invocation does). One of
		// another workspace leaves the entry as it is.
		e.job.Run, e.job.View = j.Run, j.View
	}
	e.live++
	if j.Kind == JobScript {
		r.derive(j.Env["WM_FLOW_JOB_ID"], j.Root, j.Env["WM_FLOW_PATH"], j.Run, j.Host)
		r.derive(j.Env["WM_ROOT_FLOW_JOB_ID"], j.Root, "", j.Run, j.Host)
		r.derive(j.Root, j.Root, "", j.Run, j.Host)
	}
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { r.end(e) }) }, true
}

// heldElsewhere reports whether a job of another workspace than j's holds
// j's id: a registered job, not one known only from a script job's
// variables, or an ended job the registry keeps. Called with mu held.
func (r *Registry) heldElsewhere(j Job) bool {
	if e := r.jobs[j.ID]; e != nil && !e.derived && !sameWorkspace(e.job.Host, j.Host) {
		return true
	}
	k, ok := r.ended[j.ID]
	return ok && !sameWorkspace(Host{Workspace: k.workspace}, j.Host)
}

// ExclusiveJobs is a Jobs that can refuse a registration whose id a job of
// another workspace holds (Registry.BeginExclusive).
type ExclusiveJobs interface {
	Jobs
	BeginExclusive(j Job) (end func(), ok bool)
}

// BeginExclusive registers j with jobs through its BeginExclusive when it
// has one, and with Begin otherwise, which never refuses.
func BeginExclusive(jobs Jobs, j Job) (end func(), ok bool) {
	if x, own := jobs.(ExclusiveJobs); own {
		return x.BeginExclusive(j)
	}
	return jobs.Begin(j), true
}

// derive makes a flow job of root known from a script job's variables,
// unless it is known already. It takes the script job's run, which is the
// run that holds that flow job too, unless a dispatch placed the script
// job's run in another run's tree, and the script job's host.
//
// An id that the registry keeps as an ended job of another workspace stays
// that job: a script job of a run that a dispatch tracked in it (root_job)
// does not make it a flow job of the script job's workspace, so it keeps
// answering for its own workspace while the script job runs and after.
func (r *Registry) derive(id, root, path, run string, host Host) {
	if id == "" || r.jobs[id] != nil {
		return
	}
	if k, ok := r.ended[id]; ok && !sameWorkspace(Host{Workspace: k.workspace}, host) {
		return
	}
	r.gen++
	r.jobs[id] = &regJob{job: Job{ID: id, Kind: JobFlow, Root: root, Path: path, Run: run, Host: host, Gen: r.gen}, derived: true}
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
		if e != nil {
			r.retire(e.job)
		}
	}
	if len(tree) == 0 {
		delete(r.trees, root)
	}
}

// retire keeps j, a job whose tree has ended, among the ended jobs, by its
// kind, root, path and workspace only: without its Env, its run and its
// View, so that no ended run stays reachable from the registry. It forgets
// the oldest ones beyond keep. Called with mu held.
func (r *Registry) retire(j Job) {
	r.seq++
	r.ended[j.ID] = endedJob{kind: j.Kind, root: j.Root, path: j.Path, workspace: j.Host.Workspace, seq: r.seq}
	r.order = append(r.order, endedRef{id: j.ID, seq: r.seq})
	for len(r.ended) > r.keep || len(r.order) > 2*r.keep {
		old := r.order[0]
		r.order = r.order[1:]
		if k, ok := r.ended[old.id]; ok && k.seq == old.seq {
			delete(r.ended, old.id)
		}
	}
}

// Job returns the job id names.
func (r *Registry) Job(id string) (Job, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.jobs[id]
	if e == nil {
		k, ok := r.ended[id]
		if !ok {
			return Job{}, false, false
		}
		return Job{ID: id, Kind: k.kind, Root: k.root, Path: k.path, Host: Host{Workspace: k.workspace}}, false, true
	}
	live := e.live > 0
	if e.derived {
		live = r.rootLive(e.job.Root)
	}
	j := e.job
	if j.View == nil && j.Run != "" {
		if re := r.jobs[j.Run]; re != nil {
			j.View = re.job.View
		}
	}
	return j, live, true
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
	// gen and rootGen are the registrations (Job.Gen) of the job and of
	// its root that the map was written under, and workspace the job's
	// workspace: a map is the state of that registration only, and of
	// that workspace's even with a registry that numbers none.
	gen, rootGen uint64
	workspace    string
	vals         map[string]json.RawMessage
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
// job registry reports live: a map for any other job would have no live
// root to be forgotten with.
func (u *UserStates) SetUserState(_ context.Context, job, key string, v json.RawMessage) error {
	j, live, ok := u.jobs.Job(job)
	if !ok || !live {
		return nil
	}
	u.setAt(j, key, v)
	return nil
}

// owns reports whether m is the map of the registration j.
func (m *userMap) owns(j Job) bool {
	return m != nil && m.gen == j.Gen && m.workspace == j.Host.EffectiveWorkspace()
}

// stateAt returns the value stored under key for the flow job's
// registration j (Job.Gen, in its workspace), and false when that
// registration has none.
func (u *UserStates) stateAt(j Job, key string) (json.RawMessage, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.maps[j.ID]
	if !m.owns(j) {
		return nil, false
	}
	v, ok := m.vals[key]
	return v, ok
}

// setAt stores v under key for the flow job's registration checked, and
// reports whether it did: only while the registry reports that very
// registration live, in the same workspace, checked under the map lock,
// so that a write checked against one registration never lands in
// another's map. A map of another registration of the id is dropped.
func (u *UserStates) setAt(checked Job, key string, v json.RawMessage) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	j, live, ok := u.jobs.Job(checked.ID)
	if !ok || !live || j.Gen != checked.Gen || !sameWorkspace(j.Host, checked.Host) {
		return false
	}
	m := u.maps[j.ID]
	if !m.owns(j) {
		if len(u.maps) >= u.sweep {
			u.dropDead()
		}
		root := cmp.Or(j.Root, j.ID)
		rj, _, _ := u.jobs.Job(root)
		m = &userMap{root: root, gen: j.Gen, rootGen: rj.Gen, workspace: j.Host.EffectiveWorkspace(), vals: map[string]json.RawMessage{}}
		u.maps[j.ID] = m
	}
	m.vals[key] = v
	return true
}

// statesAt returns a copy of the flow job's map for its registration j,
// nil when it has none.
func (u *UserStates) statesAt(j Job) map[string]json.RawMessage {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.maps[j.ID]
	if !m.owns(j) {
		return nil
	}
	return maps.Clone(m.vals)
}

// dropDead drops the maps of the jobs the registry no longer reports live,
// and sets the next sweep at twice the maps that remain. Called with mu
// held.
func (u *UserStates) dropDead() {
	for id, m := range u.maps {
		if j, live, ok := u.jobs.Job(id); !ok || !live || !m.owns(j) {
			delete(u.maps, id)
		}
	}
	u.sweep = max(minSweep, 2*len(u.maps))
}

// forgetRoot drops the maps of root's jobs written under its registration
// gen, once the registry no longer reports that registration live: the
// maps of a later registration of the id stay. It decides under the map
// lock, under which every map is created, so a registration that replaces
// root and writes before it runs keeps its maps.
func (u *UserStates) forgetRoot(root string, gen uint64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if rj, live, ok := u.jobs.Job(root); ok && live && rj.Gen == gen {
		return
	}
	for id, m := range u.maps {
		if m.root == root && m.rootGen == gen {
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
	return s.forgetting(j, s.Jobs.Begin(j))
}

// BeginExclusive is the Jobs service's BeginExclusive, or its Begin when it
// has none (BeginExclusive), with the same forgetting of a root's maps.
func (s servicesJobs) BeginExclusive(j Job) (func(), bool) {
	end, ok := BeginExclusive(s.Jobs, j)
	if !ok {
		return nil, false
	}
	return s.forgetting(j, end), true
}

// forgetting returns end, the end of j's registration, which for a root's
// registration also drops the maps written under it.
func (s servicesJobs) forgetting(j Job, end func()) func() {
	if s.states == nil || j.ID == "" || cmp.Or(j.Root, j.ID) != j.ID {
		return end
	}
	// The registration that this call made, whose maps its end drops.
	reg, _, _ := s.Jobs.Job(j.ID)
	var once sync.Once
	return func() {
		once.Do(func() {
			end()
			s.states.forgetRoot(j.ID, reg.Gen)
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

// JobScope returns the workspace scope of the job id names, live or ended:
// the effective workspace of its Host as the registry holds it
// (Host.EffectiveWorkspace; ADR 0007, a workspace per run); and false when
// the registry does not know the job. With the services' own registry a
// job of another scope is as unknown to a caller of this one (InScope).
func (s *Services) JobScope(id string) (string, bool) {
	j, _, ok := s.jobs().Job(id)
	return j.Host.EffectiveWorkspace(), ok
}

// sameWorkspace reports whether jobs of a and b are in one workspace.
func sameWorkspace(a, b Host) bool {
	return a.EffectiveWorkspace() == b.EffectiveWorkspace()
}

// inScope reports whether j is of the workspace scope scope; every job is
// of scope "", which a caller that is of every workspace looks up with.
func inScope(j Job, scope string) bool {
	return scope == "" || j.Host.EffectiveWorkspace() == scope
}

// InScope reports whether the services find j for a caller of the
// workspace scope scope. With their own registry, which gives each job its
// owner when it registers, j must be of that scope (inScope). A host's
// registry keeps the base's behaviour: its jobs carry no owner the
// services can trust (it may keep them as submitted, or keep only an ended
// job's id, kind, root and path), so every job it knows is found, as
// before, and the engine runs no run of another workspace than its
// configured one over it (Services.OwnsJobs).
func (s *Services) InScope(j Job, scope string) bool {
	if _, own := s.jobs().(*Registry); !own {
		return true
	}
	return inScope(j, scope)
}

// KnownIn reports whether the registry knows the job id names, for a caller
// of the workspace scope scope (InScope).
func (s *Services) KnownIn(id, scope string) bool {
	j, _, ok := s.jobs().Job(id)
	return ok && s.InScope(j, scope)
}

// RootJob answers getRootJobId (WA-14, WA-V1): the effective root of the
// job id names, and false when the registry does not know it.
func (s *Services) RootJob(id string) (string, bool) {
	return s.RootJobIn(id, "")
}

// RootJobIn is RootJob for a caller of the workspace scope scope (JobScope;
// "" for every scope): with the services' own registry a job of another
// scope is unknown (InScope). The scope and the root come from one lookup
// of the registry, so a job registered again in between cannot answer for
// another workspace.
func (s *Services) RootJobIn(id, scope string) (string, bool) {
	j, _, ok := s.jobs().Job(id)
	if !ok || !s.InScope(j, scope) {
		return "", false
	}
	return j.Root, true
}

// GetJob answers GET jobs_u/get/{id} ([WA-19]): the record of the job id
// names, which the run that holds it renders (Job.View) while the job's
// root run is live. An id the job registry does not know fails with the
// API's 404 `Not found: Job not found at name <id>`, written without
// Windmill's Rust location (D14). A job the registry knows but no live
// run holds fails with a *NotServed: a job of a run that has ended
// (ErrEndedRun, deviation D22), whose record Windmill keeps until its job
// retention removes it, 30 days by default.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
func (s *Services) GetJob(id string) ([]byte, error) {
	return s.GetJobIn(id, "")
}

// GetJobIn is GetJob for a caller of the workspace scope scope: a job of
// another scope fails as an unknown id. The record comes from the view of
// the registry entry whose scope was checked.
func (s *Services) GetJobIn(id, scope string) ([]byte, error) {
	j, err := s.liveJob(id, scope, "Job not found at name %s")
	if err != nil {
		return nil, err
	}
	return j.View.JobRecord(j)
}

// GetJobArgs answers GET jobs_u/get_args/{id} ([WA-19]): the stored
// arguments of the job id names, which the run that holds it renders
// while the job's root run is live. An id the job registry does not know
// fails with the API's 404 `Not found: Job Args not found at name <id>`,
// written without Windmill's Rust location (D14); a job of a run that has
// ended fails with ErrEndedRun (D22), as GetJob does.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
func (s *Services) GetJobArgs(id string) ([]byte, error) {
	return s.GetJobArgsIn(id, "")
}

// GetJobArgsIn is GetJobArgs for a caller of the workspace scope scope, as
// GetJobIn is GetJob's.
func (s *Services) GetJobArgsIn(id, scope string) ([]byte, error) {
	j, err := s.liveJob(id, scope, "Job Args not found at name %s")
	if err != nil {
		return nil, err
	}
	return j.View.JobArgs(id)
}

// ResultPoll answers the result polls ([WA-15], [WA-V2]) for the job id
// names from the run that holds it (JobView.JobPoll), for every job that
// GetJob answers for. One lookup of the id classifies the job, so that a
// poll sees one state of the registry for it: known is false when the
// registry does not know it. A known job that GetJob does not serve fails
// as GetJob fails: ErrEndedRun for a job of a run that has ended, a
// *NotServed for a job in a state whose record windmill-lite does not
// reproduce, such as a canceled job.
//
// [WA-15]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-15
// [WA-V2]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-v2
func (s *Services) ResultPoll(id string) (r JobResult, known bool, err error) {
	jobs := s.jobs()
	j, live, ok := jobs.Job(id)
	if !ok {
		return JobResult{}, false, nil
	}
	if j, err = liveOf(jobs, j, live, id); err != nil {
		return JobResult{}, true, err
	}
	r, err = j.View.JobPoll(id)
	return r, true, err
}

// liveJob returns the job id names when a run that is live holds it: an
// id the registry does not know, or knows in another scope than scope,
// fails with the API's 404 of format, a job whose root run has ended with
// ErrEndedRun.
func (s *Services) liveJob(id, scope, format string) (Job, error) {
	jobs := s.jobs()
	j, live, ok := jobs.Job(id)
	if !ok || !s.InScope(j, scope) {
		return Job{}, notFound(format, id)
	}
	return liveOf(jobs, j, live, id)
}

// liveOf returns j, which one lookup of id found with live, when a run
// that is live holds it, and fails with ErrEndedRun when its root has
// ended. A job that is its own root takes that lookup's live; another
// asks the registry for its root.
func liveOf(jobs Jobs, j Job, live bool, id string) (Job, error) {
	// A job of a live run is known while its root lives, running or
	// completed; once the root has ended, the registry keeps only the ids
	// of its tree's jobs.
	rootLive := live
	if root := cmp.Or(j.Root, id); root != id {
		_, rootLive, _ = jobs.Job(root)
	}
	switch {
	case !rootLive:
		return Job{}, ErrEndedRun
	case j.View == nil:
		return Job{}, &NotServed{Reason: "no run of this runtime holds the job"}
	}
	return j, nil
}

// FlowUserStateLister is a flow user state service that can list a flow
// job's whole map, which GET jobs_u/get/{id} shows as its flow status's
// user_states ([WA-19]). UserStates implements it.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
type FlowUserStateLister interface {
	// UserStates returns each key of the flow job's map with its value,
	// as the JSON text a read gives back.
	UserStates(ctx context.Context, job string) (map[string]json.RawMessage, error)
}

// UserStates returns each key of the flow job's map with its value, as a
// read gives it back.
func (u *UserStates) UserStates(_ context.Context, job string) (map[string]json.RawMessage, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.maps[job]
	if m == nil {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(m.vals))
	for k, v := range m.vals {
		out[k] = v
	}
	return out, nil
}

// FlowUserStates returns the user state of the live flow job id names,
// as the object that its flow status holds (WA-14): each key with its
// value as a read gives it back, in jsonb's order, as the flow status
// column stores it. It returns false when the job has no user state, and
// when the services' FlowUserState cannot list a map
// (FlowUserStateLister).
func (s *Services) FlowUserStates(ctx context.Context, id string) (json.RawMessage, bool, error) {
	j, live := s.liveFlowJob(id, "")
	if !live {
		return nil, false, nil
	}
	return s.FlowUserStatesOf(ctx, j)
}

// FlowUserStatesOf is FlowUserStates for the registration j, as a job read
// found it (JobView.JobRecord): it lists only while the registry still holds
// that registration live as a flow job, in its workspace (a completed child
// flow lists none, as FlowUserStates would not), and with the services' own
// store it lists that registration's map and none of another's.
func (s *Services) FlowUserStatesOf(ctx context.Context, j Job) (json.RawMessage, bool, error) {
	l, ok := s.userState().(FlowUserStateLister)
	if !ok {
		return nil, false, nil
	}
	if cur, live, known := s.jobs().Job(j.ID); !known || !live || cur.Kind != JobFlow || cur.Gen != j.Gen || !sameWorkspace(cur.Host, j.Host) {
		return nil, false, nil
	}
	var m map[string]json.RawMessage
	var err error
	if u, own := l.(*UserStates); own {
		m = u.statesAt(j)
	} else {
		m, err = l.UserStates(ctx, j.ID)
	}
	if err != nil || len(m) == 0 {
		return nil, false, err
	}
	var b []byte
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b = append(b, ',')
		q, err := json.Marshal(k)
		if err != nil {
			return nil, false, err
		}
		b = append(b, q...)
		b = append(b, ':')
		b = append(b, m[k]...)
	}
	b[0] = '{'
	stored, err := jsonval.FromProducer(append(b, '}')).Stored()
	if err != nil {
		return nil, false, err
	}
	return stored, true, nil
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
	return s.ReadJobEnvIn(id, workspace, "")
}

// ReadJobEnvIn is ReadJobEnv for a caller of the workspace scope scope: a
// job of another scope fails as an unknown id, whatever its job variables
// say, and so does a live flow job of another scope. The job's workspace
// that the path must name is, with the services' own registry, its run's
// configured one when it has one, and else, as with a host's registry, its
// WM_WORKSPACE, which a hook may have set.
func (s *Services) ReadJobEnvIn(id, workspace, scope string) (map[string]string, error) {
	j, live, ok := s.jobs().Job(id)
	own := j.Env["WM_WORKSPACE"]
	if _, reg := s.jobs().(*Registry); reg {
		own = cmp.Or(j.Host.Workspace, own)
	}
	switch {
	case !ok || !live || !s.InScope(j, scope) || workspace != "" && own != "" && own != workspace:
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
	return s.FlowUserStateIn(ctx, id, key, "")
}

// FlowUserStateIn is FlowUserState for a caller of the workspace scope
// scope: a flow job of another scope reads null, as an unknown one does.
// With the services' own store a read gives the map of the registration
// it checked (Job.Gen, in its workspace) and none other. With a host's
// store, which serves its engine's configured workspace only (OwnsJobs),
// the job is looked up again after the read: a value read while the id
// stopped naming that registration reads null.
func (s *Services) FlowUserStateIn(ctx context.Context, id, key, scope string) (json.RawMessage, error) {
	j, ok := s.liveFlowJob(id, scope)
	if !ok {
		return json.RawMessage("null"), nil
	}
	if u, own := s.userState().(*UserStates); own {
		if v, ok := u.stateAt(j, key); ok {
			return v, nil
		}
		return json.RawMessage("null"), nil
	}
	v, ok, err := s.userState().UserState(ctx, id, key)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return json.RawMessage("null"), nil
	}
	if again, ok := s.liveFlowJob(id, scope); !ok || again.Gen != j.Gen || !sameWorkspace(again.Host, j.Host) {
		return json.RawMessage("null"), nil
	}
	return v, nil
}

// OwnsJobs reports whether the services keep jobs and flow user state in
// their own job registry and store (Registry, UserStates), as wl's do, and
// not in a host's Jobs or FlowUserState. Only these keep workspaces apart
// (BeginExclusive, a map per registration and workspace), so the engine
// refuses a run in a workspace other than its configured one without them.
func (s *Services) OwnsJobs() bool {
	_, reg := s.jobs().(*Registry)
	_, states := s.userState().(*UserStates)
	return reg && states
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
	return s.SetFlowUserStateIn(ctx, id, key, body, "")
}

// SetFlowUserStateIn is SetFlowUserState for a caller of the workspace
// scope scope: a flow job of another scope fails as an unknown one does,
// after the value's own check, in the same order.
func (s *Services) SetFlowUserStateIn(ctx context.Context, id, key string, body []byte, scope string) error {
	v, err := userStateValue(body)
	if err != nil {
		return &Error{Variant: "BadRequest", Message: "windmill-lite: the flow user state value: " + err.Error()}
	}
	j, ok := s.liveFlowJob(id, scope)
	if !ok {
		return ErrFlowJobNotFound
	}
	if u, own := s.userState().(*UserStates); own {
		// Into the map of the registration that was checked, which must
		// still be the registry's when the write lands.
		if !u.setAt(j, key, v) {
			return ErrFlowJobNotFound
		}
		return nil
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

// JSONSyntaxError reports why text is not one JSON text, in the words of
// Windmill's backend JSON library ("<message> at line <l> column <c>"),
// or nil when it is one: the message of the 400 Windmill's web framework
// answers for a request body it cannot parse as JSON.
func JSONSyntaxError(text []byte) error {
	_, err := parseJSON(text)
	return err
}

func (s *Services) liveFlow(id, scope string) bool {
	_, ok := s.liveFlowJob(id, scope)
	return ok
}

// liveFlowJob returns the job id names when it is a live flow job of the
// workspace scope scope.
func (s *Services) liveFlowJob(id, scope string) (Job, bool) {
	j, live, ok := s.jobs().Job(id)
	return j, ok && live && j.Kind == JobFlow && s.InScope(j, scope)
}
