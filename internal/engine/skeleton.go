package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// Skeleton is what remains of the walking skeleton (milestone W1 of
// ADR 0012): the loader of W1's hand-written bundle, which `windmill-lite
// host` serves until F3a moves host mode onto the wl library and its
// bundles. The interpreter is the engine core's (N1a): a Skeleton turns
// each flow of its bundle into the IR and runs it on an Engine.
//
// It loads the subset W1 ran: script steps by path and inline Bun steps,
// their static, javascript and ai input transforms, and a static step
// timeout. Anything else fails at load with ErrSkeletonUnsupported. A
// Skeleton is safe for concurrent use.
type Skeleton struct {
	eng *Engine
}

// SkeletonConfig configures a Skeleton.
type SkeletonConfig struct {
	// BundleDir holds the flows, as flows/<flow path>.flow.json: the
	// OpenFlow document of each flow as JSON. Its scripts are named by
	// their job paths: scripts/bun/<path>.mjs for a path script, and
	// scripts/bun/<flow path>/<id>.mjs for an inline step (WR-29). Bundle
	// is the bundle's digest, recorded in every trace.
	BundleDir string
	Bundle    string
	// Exec runs the steps unless a run names its own; JS evaluates
	// expressions.
	Exec StepExecutor
	JS   expr.Engine
	// Workspace is WM_WORKSPACE; empty means "windmill-lite".
	Workspace string
	// InstanceTimeout is the instance's maximum job duration (WR-37); zero
	// means Windmill's self-hosted default of 604800 s.
	InstanceTimeout time.Duration
}

// ErrSkeletonUnsupported marks a flow that uses a feature the walking
// skeleton's bundle format does not carry.
var ErrSkeletonUnsupported = errors.New("not supported by the walking skeleton")

// NewSkeleton loads every flow of cfg.BundleDir and opens them on an
// Engine.
func NewSkeleton(cfg SkeletonConfig) (*Skeleton, error) {
	if cfg.JS == nil {
		return nil, errors.New("engine: the skeleton needs JS")
	}
	prog := &ir.Program{Version: ir.Version, Flows: map[string]*ir.Flow{}, Scripts: map[string]*ir.Script{}}
	root := filepath.Join(cfg.BundleDir, "flows")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".flow.json") {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		path := filepath.ToSlash(strings.TrimSuffix(rel, ".flow.json"))
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		doc, err := openflow.DecodeFlow(b)
		if err != nil {
			return fmt.Errorf("engine: flow %s: %w", path, err)
		}
		f, err := skeletonFlow(prog, path, doc)
		if err != nil {
			return fmt.Errorf("engine: flow %s: %w", path, err)
		}
		prog.Flows[path] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	eng, err := New(Config{Program: prog, Bundle: cfg.Bundle, Exec: cfg.Exec, JS: cfg.JS, Workspace: cfg.Workspace, InstanceTimeout: cfg.InstanceTimeout})
	if err != nil {
		return nil, err
	}
	return &Skeleton{eng: eng}, nil
}

func skeletonUnsupported(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrSkeletonUnsupported)
}

// skeletonFlow turns a flow of W1's bundle into the IR, adding its
// scripts to prog's inventory under their job paths, which name them in
// the bundle.
func skeletonFlow(prog *ir.Program, path string, doc *openflow.Flow) (*ir.Flow, error) {
	v := doc.Value
	switch {
	case v.FailureModule != nil:
		return nil, skeletonUnsupported("a failure module")
	case v.PreprocessorModule != nil:
		return nil, skeletonUnsupported("a preprocessor")
	case v.SkipExpr != nil || v.EarlyReturn != nil || v.CacheTTL != nil || len(v.FlowEnv) > 0:
		return nil, skeletonUnsupported("skip_expr, early_return, cache_ttl and flow_env")
	}
	f := &ir.Flow{Path: path}
	seen := map[string]bool{}
	for _, m := range v.Modules {
		if seen[m.ID] {
			return nil, skeletonUnsupported("module id %q occurs twice", m.ID)
		}
		seen[m.ID] = true
		mod, err := skeletonModule(prog, path, m)
		if err != nil {
			return nil, fmt.Errorf("module %q: %w", m.ID, err)
		}
		f.Modules = append(f.Modules, mod)
	}
	return f, nil
}

func skeletonModule(prog *ir.Program, flowPath string, m openflow.Module) (*ir.Module, error) {
	switch {
	case m.SkipIf != nil, m.StopAfterIf != nil, m.StopAfterAllItersIf != nil, m.Sleep != nil, m.Suspend != nil,
		m.CacheTTL != nil, m.Retry != nil && m.Retry.MaxRetries() > 0, m.ContinueOnError != nil && *m.ContinueOnError,
		m.Mock != nil && m.Mock.Enabled, m.PassFlowInputDirectly != nil && *m.PassFlowInputDirectly:
		return nil, skeletonUnsupported("skip_if, stop_after_if, sleep, suspend, cache_ttl, retry, continue_on_error, mock and pass_flow_input_directly")
	}
	mod := &ir.Module{ID: m.ID}
	if t := m.Timeout; t != nil {
		if t.Type != openflow.Static {
			return nil, skeletonUnsupported("a %s timeout", t.Type)
		}
		var secs *int64
		if err := json.Unmarshal(t.Value, &secs); err != nil {
			return nil, fmt.Errorf("timeout %s: %w", t.Value, err)
		}
		if secs != nil && *secs < 0 {
			return nil, skeletonUnsupported("a negative timeout")
		}
		mod.Timeout = &ir.Transform{Type: ir.Static, Value: t.Value}
	}
	var script string
	var transforms openflow.Transforms
	switch val := m.Value.(type) {
	case *openflow.PathScript:
		mod.Kind, script, transforms = ir.KindScript, val.Path, val.InputTransforms
	case *openflow.RawScript:
		if val.Language != openflow.LangBun {
			return nil, skeletonUnsupported("%s steps", val.Language)
		}
		// The bundle holds the inline script's module under the job path
		// Windmill gives it (WR-29).
		mod.Kind, script, transforms = ir.KindRawScript, flowPath+"/"+m.ID, val.InputTransforms
	default:
		return nil, skeletonUnsupported("%s modules", m.Value.Kind())
	}
	leaf := &ir.Leaf{Script: script, Harness: ir.ClassBun, Boundary: ir.BoundaryBun, Inputs: []ir.Input{}}
	for _, nt := range transforms {
		in := ir.Input{Name: nt.Name}
		switch nt.Transform.Type {
		case openflow.Static:
			in.Transform = ir.Transform{Type: ir.Static, Value: nt.Transform.Value}
		case openflow.JavaScript:
			in.Transform = ir.Transform{Type: ir.Javascript, Expr: expr.Compile(expr.SiteInputTransform, nt.Transform.Expr)}
		default:
			in.Transform = ir.Transform{Type: ir.AI}
		}
		leaf.Inputs = append(leaf.Inputs, in)
	}
	mod.Leaf = leaf
	prog.Scripts[script] = &ir.Script{ID: script, Path: script, Language: openflow.LangBun, Harness: ir.ClassBun, Boundary: ir.BoundaryBun}
	return mod, nil
}

// SkeletonRun is one run of a flow.
type SkeletonRun struct {
	// ID is the run's id, which steps see as WM_FLOW_JOB_ID; empty means a
	// new UUID. In host mode it is the id of the host's run frame, so that
	// the host can re-enter the scope it captured (H11).
	ID    string
	Path  string
	Args  jsonval.Value
	Hooks Hooks
	// Exec runs the run's steps; nil means SkeletonConfig.Exec. In host
	// mode it is the host's connection.
	Exec StepExecutor
}

// Run runs a flow to its end on the engine and returns its trace. ctx is
// the run's cancellation and deadline (H4). An error means the run could
// not start.
func (s *Skeleton) Run(ctx context.Context, r SkeletonRun) (*Trace, error) {
	return s.eng.Run(ctx, RunRequest{ID: r.ID, Path: r.Path, Args: r.Args, Hooks: r.Hooks, Exec: r.Exec})
}
