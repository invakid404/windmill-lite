package ir

import (
	"errors"
	"fmt"
	"slices"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// Options are the compiler's build options.
type Options struct {
	// Strict rejects an absent for-loop `skip_failures` or branchall branch
	// `skip_failure` instead of applying the deployed default, for authors
	// who want the folder to say what runs (Q-CF-13, 0001 §7).
	Strict bool
}

// virtualSummary is the summary Windmill gives the `<id>-v` step (OF-17).
const virtualSummary = "Virtual module needed for suspend/sleep when last module"

// Compile turns a sync folder into the program the engine runs (0002). It
// walks the dependency closure from the contract's roots, stopping at the
// contract's provided sites, and applies once what a successful deploy
// would have applied: container defaults, the bun/bunnative switch, the
// virtual `<id>-v` steps, and the dropped fields. It then validates what is
// reachable. With a nil contract, every flow and script the folder lists
// is a root and nothing is provided.
//
// Compile reports every error it finds, joined, in walk order; each is an
// *Error. It returns a program only when there is none.
func Compile(f *syncfolder.Folder, contract *Contract, opts Options) (*Program, error) {
	if contract == nil {
		contract = &Contract{}
		for _, p := range f.Paths(syncfolder.KindFlow) {
			contract.Roots = append(contract.Roots, Root{Flow: p})
		}
		for _, p := range f.Paths(syncfolder.KindScript) {
			contract.Roots = append(contract.Roots, Root{Script: p})
		}
		if len(contract.Roots) == 0 {
			return nil, contractError("the folder has no flows or scripts to compile")
		}
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	c := &compiler{
		folder:   f,
		contract: contract,
		opts:     opts,
		prog: &Program{
			Version:    Version,
			Contract:   *contract,
			Flows:      map[string]*Flow{},
			Scripts:    map[string]*Script{},
			Deviations: []string{},
		},
		queued:  map[string]bool{},
		docs:    map[string]*syncfolder.Flow{},
		matches: make([][]string, len(contract.Provided)),
		flagged: map[int]bool{},
	}
	c.prog.Roots.Flows = []string{}
	c.prog.Roots.Scripts = []string{}
	for _, r := range contract.Roots {
		if r.Flow != "" {
			c.prog.Roots.Flows = append(c.prog.Roots.Flows, r.Flow)
			c.enqueue(r.Flow)
			continue
		}
		c.prog.Roots.Scripts = append(c.prog.Roots.Scripts, r.Script)
		s, err := f.Script(r.Script)
		if err != nil {
			c.fail(&Error{Kind: ErrLoad, Script: r.Script, Selector: -1, Err: err})
			continue
		}
		c.pathScript(s, "", "")
	}
	for len(c.queue) > 0 {
		p := c.queue[0]
		c.queue = c.queue[1:]
		c.flow(p)
	}
	c.checkMatches()
	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	c.deviations()
	return c.prog, nil
}

type compiler struct {
	folder   *syncfolder.Folder
	contract *Contract
	opts     Options
	prog     *Program
	errs     []error
	queue    []string
	queued   map[string]bool
	// docs holds the flow documents that a step's target check already
	// read, for the walk to compile.
	docs map[string]*syncfolder.Flow
	// matches holds, per provided site, the sites its selector matched,
	// as "<flow path> <site path>".
	matches [][]string
	// flagged records the provided sites already reported as ambiguous.
	flagged map[int]bool
	derived bool
}

func (c *compiler) fail(e *Error) { c.errs = append(c.errs, e) }

func (c *compiler) enqueue(p string) {
	if !c.queued[p] {
		c.queued[p] = true
		c.queue = append(c.queue, p)
	}
}

// scope is where a module list sits within its flow.
type scope struct {
	flow string
	// site is the static site path of the list's container: the root, a
	// container module, a branch, or a slot. Iteration segments are left
	// out, since a list's modules are the same in every iteration.
	site conformance.SitePath
	// nested is true inside a container body.
	nested bool
	// hasFailure reports a failure module in the flow, which keeps every
	// loop from collapsing (FX-46).
	hasFailure bool
	// inline is true inside the bodies of the preprocessor module, which a
	// deploy does not reduce to stored nodes, so they run as raw flows
	// (OF-3, CF-V5).
	inline bool
	// failureSlot is true for the failure module itself, whose input
	// transforms have their own scope (JE-35).
	failureSlot bool
	// preprocessorSlot is true for the preprocessor module itself.
	preprocessorSlot bool
	// slot is true in the failure and preprocessor slots, inside the
	// containers that fill them too: a deploy keeps them as raw module
	// values, where it reduces the module lists to stored nodes (WR-29).
	slot bool
}

// flow compiles the flow at p. Its document is read with its inline
// references unresolved: each rawscript's files are read only when the
// module is compiled as a step, so a provided rawscript's files are never
// read (ADR 0002, host contract).
func (c *compiler) flow(p string) {
	fl, ok := c.docs[p]
	if !ok {
		var err error
		if fl, err = c.folder.FlowDocument(p); err != nil {
			c.fail(&Error{Kind: ErrLoad, Flow: p, Selector: -1, Err: err})
			return
		}
	}
	doc := fl.Document
	v := doc.Value
	out := &Flow{
		Path:              p,
		Summary:           doc.Summary,
		Description:       doc.Description,
		Schema:            doc.Schema,
		FlowEnv:           []EnvVar{},
		EarlyReturn:       v.EarlyReturn,
		CacheTTL:          v.CacheTTL,
		CacheIgnoreS3Path: v.CacheIgnoreS3Path,
		SameWorker:        v.SameWorker,
		Timeout:           doc.Timeout,
	}
	if v.SkipExpr != nil {
		out.SkipExpr = compileExpr(expr.SiteSkipExpr, *v.SkipExpr)
	}
	for _, e := range v.FlowEnv {
		out.FlowEnv = append(out.FlowEnv, EnvVar{Name: e.Name, Value: e.Value})
	}
	fc := &flowCompiler{compiler: c, doc: fl}
	sc := scope{flow: p, hasFailure: v.FailureModule != nil}
	out.Modules = fc.list(v.Modules, sc)
	if m := v.FailureModule; m != nil {
		s := sc
		s.failureSlot, s.slot = true, true
		out.FailureModule = fc.module(m, conformance.FailureSlot(), s)
	}
	if m := v.PreprocessorModule; m != nil {
		s := sc
		s.preprocessorSlot, s.slot = true, true
		out.PreprocessorModule = fc.module(m, conformance.PreprocessorSlot(), s)
	}
	c.prog.Flows[p] = out
}

// flowCompiler compiles the modules of one flow.
type flowCompiler struct {
	*compiler
	// doc is the flow as read, its inline references resolved one
	// rawscript at a time.
	doc *syncfolder.Flow
}

// list compiles a module list. A list whose last module has a sleep gets
// Windmill's virtual identity `<id>-v` (OF-17, FX-4); occurrences of
// repeated ids are counted over the final list, virtual step included,
// since a run's sites are those of the final list (0011).
func (fc *flowCompiler) list(ms []openflow.Module, sc scope) []*Module {
	ids := make([]string, 0, len(ms)+1)
	for i := range ms {
		ids = append(ids, ms[i].ID)
	}
	virtual := len(ms) > 0 && ms[len(ms)-1].Sleep != nil
	if virtual {
		ids = append(ids, ms[len(ms)-1].ID+"-v")
	}
	segs := conformance.ModuleSegments(ids)
	out := make([]*Module, 0, len(ids))
	for i := range ms {
		out = append(out, fc.module(&ms[i], segs[i], sc))
	}
	if virtual {
		summary := virtualSummary
		out = append(out, &Module{ID: ids[len(ids)-1], Kind: KindIdentity, Virtual: true, Summary: &summary})
	}
	for i, m := range out {
		if k, ok := segs[i].Occurrence(); ok {
			m.Occurrence = &k
		}
	}
	return out
}

// module compiles one module, whose segment in its list is seg.
func (fc *flowCompiler) module(m *openflow.Module, seg conformance.Segment, sc scope) *Module {
	site := sc.site.Child(seg)
	out := &Module{ID: m.ID, DerivedID: m.IDDerived, Summary: m.Summary}
	if m.IDDerived {
		fc.derived = true
	}
	fc.envelope(m, out, sc)
	if p := fc.provided(m, seg, site, sc); p != nil {
		out.Kind, out.Provided = KindProvided, p
		return out
	}
	in := inputSite(sc)
	body := sc
	body.site, body.nested, body.failureSlot = site, true, false
	if sc.preprocessorSlot {
		body.inline, body.preprocessorSlot = true, false
	}
	switch v := m.Value.(type) {
	case *openflow.RawScript:
		out.Kind = KindRawScript
		out.Leaf = fc.rawScript(m, v, site, sc, inputs(v.InputTransforms, in))
	case *openflow.PathScript:
		out.Kind = KindScript
		out.Leaf = fc.scriptStep(m, v, inputs(v.InputTransforms, in), sc)
	case *openflow.PathFlow:
		out.Kind = KindSubflow
		out.Subflow = &Subflow{Path: v.Path, Inputs: inputs(v.InputTransforms, in)}
		fc.subflow(m, v, sc)
	case *openflow.Identity:
		out.Kind = KindIdentity
	case *openflow.ForLoop:
		out.Kind = KindForLoop
		if v.SkipFailures == nil && fc.opts.Strict {
			fc.fail(&Error{Kind: ErrAbsentSkipFlag, Flow: sc.flow, Step: m.ID, Selector: -1,
				Msg: "strict: the for-loop's skip_failures is absent (the deployed default is true)"})
		}
		l := &ForLoop{
			Iterator:     transform(v.Iterator, expr.SiteIterator),
			Modules:      fc.list(v.Modules, body),
			SkipFailures: v.EffectiveSkipFailures(),
			Parallel:     v.Parallel,
			Squash:       v.Squash != nil && *v.Squash,
			Collapse:     collapses(true, v.Parallel, v.Modules, sc),
		}
		if v.Parallelism != nil {
			t := transform(*v.Parallelism, expr.SiteParallelism)
			l.Parallelism = &t
		}
		out.ForLoop = l
	case *openflow.WhileLoop:
		out.Kind = KindWhileLoop
		out.WhileLoop = &WhileLoop{
			Modules:      fc.list(v.Modules, body),
			SkipFailures: v.SkipFailures,
			Squash:       v.Squash != nil && *v.Squash,
			Collapse:     collapses(false, false, v.Modules, sc),
		}
	case *openflow.BranchOne:
		out.Kind = KindBranchOne
		b := &BranchOne{Branches: []OneBranch{}}
		for i, br := range v.Branches {
			bs := body
			bs.site = site.Child(conformance.Branch(i))
			b.Branches = append(b.Branches, OneBranch{
				Summary: br.Summary,
				Expr:    compileExpr(expr.SiteBranchOne, br.Expr),
				Modules: fc.list(br.Modules, bs),
			})
		}
		ds := body
		ds.site = site.Child(conformance.DefaultBranch())
		b.Default = fc.list(v.Default, ds)
		out.BranchOne = b
	case *openflow.BranchAll:
		out.Kind = KindBranchAll
		b := &BranchAll{Branches: []AllBranch{}, Parallel: v.Parallel}
		for i, br := range v.Branches {
			if br.SkipFailure == nil && fc.opts.Strict {
				fc.fail(&Error{Kind: ErrAbsentSkipFlag, Flow: sc.flow, Step: m.ID, Selector: -1,
					Msg: fmt.Sprintf("strict: branch %d's skip_failure is absent (the deployed default is true)", i)})
			}
			bs := body
			bs.site = site.Child(conformance.Branch(i))
			b.Branches = append(b.Branches, AllBranch{
				Summary:     br.Summary,
				Modules:     fc.list(br.Modules, bs),
				SkipFailure: br.EffectiveSkipFailure(),
			})
		}
		out.BranchAll = b
	case *openflow.AIAgent:
		fc.fail(unsupported(sc.flow, m.ID, "aiagent"))
	case *openflow.FlowScript:
		// The lite form's internal reference never appears in a sync
		// folder; a folder that holds one cannot be pushed as written.
		fc.fail(unsupported(sc.flow, m.ID, "flowscript"))
	default:
		panic(fmt.Sprintf("ir: unknown module value %T", m.Value))
	}
	return out
}

// envelope copies the envelope fields that matter at run time. Queue,
// placement and cleanup fields (priority, debouncing, delete_after_use,
// delete_after_secs) have no effect and are dropped (0001 §3, OF-12).
func (fc *flowCompiler) envelope(m *openflow.Module, out *Module, sc scope) {
	if m.Suspend != nil {
		// Durable suspend is out of scope (FX-39). A suspend on a provided
		// site's own envelope belongs to this flow, not to the opaque
		// target, so it is refused too.
		fc.fail(unsupported(sc.flow, m.ID, "suspend"))
	}
	if m.SkipIf != nil {
		out.SkipIf = compileExpr(expr.SiteSkipIf, m.SkipIf.Expr)
	}
	out.StopAfterIf = stop(m.StopAfterIf, expr.SiteStopAfterIf)
	out.StopAfterAllItersIf = stop(m.StopAfterAllItersIf, expr.SiteStopAfterAllItersIf)
	if m.Sleep != nil {
		t := transform(*m.Sleep, expr.SiteSleep)
		out.Sleep = &t
	}
	if m.Timeout != nil {
		t := transform(*m.Timeout, expr.SiteTimeout)
		out.Timeout = &t
	}
	if r := m.Retry; r != nil {
		out.Retry = &Retry{}
		if k := r.Constant; k != nil {
			out.Retry.Constant = &ConstantRetry{Attempts: k.Attempts, Seconds: k.Seconds}
		}
		if e := r.Exponential; e != nil {
			out.Retry.Exponential = &ExponentialRetry{Attempts: e.Attempts, Multiplier: e.Multiplier, Seconds: e.Seconds, RandomFactor: e.RandomFactor}
		}
		if r.RetryIf != nil {
			out.Retry.RetryIf = compileExpr(expr.SiteRetryIf, r.RetryIf.Expr)
		}
	}
	out.ContinueOnError = m.ContinueOnError != nil && *m.ContinueOnError
	if m.Mock != nil {
		out.Mock = &Mock{Enabled: m.Mock.Enabled, ReturnValue: m.Mock.ReturnValue}
	}
	out.CacheTTL = m.CacheTTL
	out.CacheIgnoreS3Path = m.CacheIgnoreS3Path
	out.ApplyPreprocessor = m.ApplyPreprocessor
	out.PassFlowInputDirectly = m.PassFlowInputDirectly != nil && *m.PassFlowInputDirectly
}

// subflow checks a by-path flow step's target and queues it. The target
// is read here so that an error names the step (SY-15), with its inline
// references unresolved, as flow reads it.
func (fc *flowCompiler) subflow(m *openflow.Module, v *openflow.PathFlow, sc scope) {
	if fc.queued[v.Path] {
		return
	}
	item, err := fc.folder.TargetDocument(sc.flow, m)
	if err != nil {
		fc.fail(&Error{Kind: ErrLoad, Flow: sc.flow, Step: m.ID, Selector: -1, Err: err})
		return
	}
	fc.docs[v.Path] = item.(*syncfolder.Flow)
	fc.enqueue(v.Path)
}

// scriptStep checks a by-path script step and adds its target to the
// inventory.
func (fc *flowCompiler) scriptStep(m *openflow.Module, v *openflow.PathScript, in []Input, sc scope) *Leaf {
	leaf := &Leaf{Script: v.Path, Inputs: in}
	if v.Hash != nil {
		// A folder holds one version per path and never says which hash
		// Windmill gave it, so no pin can be shown to be the folder's
		// version (OF-14, SY-15). A provided site can stand in for it.
		fc.fail(&Error{Kind: ErrPinnedHash, Flow: sc.flow, Step: m.ID, Script: v.Path, Selector: -1,
			Msg: fmt.Sprintf("script hash %s pins a version of %s that the folder cannot supply", v.Hash, v.Path)})
		return leaf
	}
	item, err := fc.folder.Target(sc.flow, m)
	if err != nil {
		fc.fail(&Error{Kind: ErrLoad, Flow: sc.flow, Step: m.ID, Script: v.Path, Selector: -1, Err: err})
		return leaf
	}
	s := fc.pathScript(item.(*syncfolder.Script), sc.flow, m.ID)
	if s != nil {
		leaf.Script, leaf.Harness, leaf.Boundary = s.ID, s.Harness, s.Boundary
	}
	return leaf
}

// collapses decides whether a loop runs each iteration as a job of the
// enclosing flow (FX-46, CF-8, CF-V5). windmill-lite runs the deployed lite
// form, where container bodies are stored nodes, so the deployed rule holds
// at every depth (0001 §1): a sequential for loop, or any while loop, whose
// body is exactly one script or rawscript step with none of sleep,
// suspend, cache_ttl, retry, stop_after_if, stop_after_all_iters_if,
// skip_if or an enabled mock, in a flow with no failure module. The
// preprocessor module's bodies are not reduced to nodes, so they run as raw
// flows, in which a for loop never collapses.
//
// The body is judged as written. A body module that is a provided site
// keeps its original kind for this rule, so the orchestration around the
// site is the one its real target would get.
func collapses(forLoop, parallel bool, body []openflow.Module, sc scope) bool {
	if sc.hasFailure || len(body) != 1 {
		return false
	}
	if forLoop && (parallel || sc.inline) {
		return false
	}
	b := &body[0]
	switch b.Value.(type) {
	case *openflow.PathScript, *openflow.RawScript, *openflow.FlowScript:
	default:
		return false
	}
	return b.Sleep == nil && b.Suspend == nil && b.CacheTTL == nil && b.Retry == nil &&
		b.StopAfterIf == nil && b.StopAfterAllItersIf == nil && b.SkipIf == nil &&
		(b.Mock == nil || !b.Mock.Enabled)
}

// compileExpr is the compiled expression at a site: the expression
// pipeline's textual stages (trimming, shortcuts, rewrites, mode, bound
// names and the engine source, JE-5 to JE-15), which depend on the site and
// the text alone, so the program carries them and nothing recomputes them
// per run (ADR 0003). The engine compiles each Source to bytecode when it
// opens the program.
func compileExpr(site expr.SiteKind, text string) *expr.Expr {
	return expr.Compile(site, text)
}

func transform(t openflow.InputTransform, site expr.SiteKind) Transform {
	switch t.Type {
	case openflow.Static:
		return Transform{Type: Static, Value: t.Value}
	case openflow.JavaScript:
		return Transform{Type: Javascript, Expr: compileExpr(site, t.Expr)}
	case openflow.AI:
		return Transform{Type: AI}
	}
	panic("ir: unknown transform type " + string(t.Type))
}

func inputs(ts openflow.Transforms, site expr.SiteKind) []Input {
	out := []Input{}
	for _, t := range ts {
		out = append(out, Input{Name: t.Name, Transform: transform(t.Transform, site)})
	}
	return out
}

func stop(s *openflow.StopAfterIf, site expr.SiteKind) *Stop {
	if s == nil {
		return nil
	}
	return &Stop{
		Expr:               compileExpr(site, s.Expr),
		SkipIfStopped:      s.SkipIfStopped,
		ErrorMessage:       s.ErrorMessage,
		ErrorIncludeResult: s.ErrorIncludeResult,
	}
}

// deviations lists the deviations the program's content puts in effect.
func (c *compiler) deviations() {
	set := map[string]bool{}
	if c.derived {
		set["D6"] = true
	}
	if len(c.contract.HostModules) > 0 {
		set["D12"] = true
	}
	for _, s := range c.prog.Scripts {
		if s.Boundary == BoundaryNative {
			set["D5"] = true
		}
		if s.Bundling == BundlingNone {
			set["D7"] = true
		}
		if s.Codebase != nil && s.Codebase.Format == "cjs" {
			set["D13"] = true
		}
	}
	for d := range set {
		c.prog.Deviations = append(c.prog.Deviations, d)
	}
	slices.SortFunc(c.prog.Deviations, func(a, b string) int {
		return deviationNumber(a) - deviationNumber(b)
	})
}

func deviationNumber(d string) int {
	n := 0
	fmt.Sscanf(d, "D%d", &n)
	return n
}
