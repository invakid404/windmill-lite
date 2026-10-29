package oracle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// StubReport lists what StubTree rewrote.
type StubReport struct {
	// Files are the rewritten script sources, relative to the tree's root,
	// sorted.
	Files []string `json:"files"`
}

// ErrStubUnsupported is the refusal of a script or step that stub mode
// cannot replace faithfully: file names the file, step the flow step, and
// reason says why.
func ErrStubUnsupported(file, step, reason string) *Refusal {
	kv := []string{}
	if file != "" {
		kv = append(kv, "file", jsonString(file))
	}
	if step != "" {
		kv = append(kv, "step", jsonString(step))
	}
	return refusal("stub_unsupported", append(kv, "reason", jsonString(reason))...)
}

// StubTree copies the sync tree at src (the directory of its wmill.yaml)
// to dst, which must not exist, and replaces the source of every Bun
// script and every inline Bun script of a flow with its stub (StubSource),
// as stub mode requires (ADR 0011). Everything else is copied unchanged:
// the CLI's metadata (locks, schemas, wmill-lock.yaml) then describes the
// original, so `generate-metadata` must run on dst before it is pushed;
// CheckStubSignatures confirms afterwards that the stub kept every
// script's schema.
//
// It refuses (stub_unsupported) a tree in which some executed script
// would not be stubbed: a script in another language, a Bun script with
// the native annotation, a rawscript whose content is written in
// flow.yaml rather than in a file, a step whose target is a hub path or
// is not in the tree, a script step pinned by hash, a flowscript step,
// and AI-agent steps; a retry on a loop or branch step, whose failed
// attempt the walk keeps as its last failed child alone, under r:<k>
// without the child's i: or b: segment, so that none of its calls matches
// the site the replay service resolved, and a loop's other children are
// not walked (CF-22); and a source StubSource refuses. A retry on a flow step is accepted (milestone N5):
// each attempt's subflow is walked whole below the step's r:<k>, and the
// replay service counts its steps' jobs across the attempts
// (conformance.CheckReplay).
func StubTree(src, dst string) (*StubReport, error) {
	if _, err := os.Stat(filepath.Join(src, "wmill.yaml")); err != nil {
		return nil, fmt.Errorf("oracle: %s is not a sync tree root (no wmill.yaml): %w", src, err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil, fmt.Errorf("oracle: stub output %s already exists", dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	created, err := outsideTree(src, dst)
	if err != nil {
		return nil, err
	}
	folder, err := syncfolder.Open(src)
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	stubs := map[string]string{} // root-relative file -> stub source
	stub := func(file, step, content string) error {
		s, err := StubSource(content)
		if err != nil {
			var se *stubError
			if errors.As(err, &se) {
				return ErrStubUnsupported(file, step, se.reason)
			}
			return err
		}
		stubs[file] = s
		return nil
	}
	for _, p := range folder.Paths(syncfolder.KindScript) {
		s, err := folder.Script(p)
		if err != nil {
			return nil, err
		}
		if err := stubbable(s.Language, s.Content); err != "" {
			return nil, ErrStubUnsupported(s.File, "", err)
		}
		if err := stub(s.File, "", s.Content); err != nil {
			return nil, err
		}
	}
	for _, p := range folder.Paths(syncfolder.KindFlow) {
		fl, err := folder.Flow(p)
		if err != nil {
			return nil, err
		}
		files := map[*openflow.RawScript]string{}
		for _, in := range fl.Inline {
			files[in.Script] = in.ContentFile
		}
		for m := range fl.Document.Value.AllModules() {
			step := fmt.Sprintf("flow %s step '%s'", p, m.ID)
			if _, flowStep := m.Value.(*openflow.PathFlow); m.Retry != nil && m.Retry.MaxRetries() > 0 && !leafModule(m) && !flowStep {
				return nil, ErrStubUnsupported(fl.File, step, "a retry on a loop or branch step: the walk keeps a failed attempt as its last failed child alone, under r:<k> without the child's i: or b: segment, so none of its calls matches the site the replay service resolved, and a loop's other children are not walked")
			}
			switch v := m.Value.(type) {
			case *openflow.RawScript:
				file, ok := files[v]
				if !ok {
					return nil, ErrStubUnsupported(fl.File, step, "the rawscript's content is written in flow.yaml, not in an !inline file the stub can replace")
				}
				if err := stubbable(v.Language, v.Content); err != "" {
					return nil, ErrStubUnsupported(file, step, err)
				}
				if err := stub(file, step, v.Content); err != nil {
					return nil, err
				}
			case *openflow.PathScript, *openflow.PathFlow:
				// The target must be an item of this tree, so that it is
				// stubbed (or walked) too: a hub path or a workspace item
				// outside the tree would run its real body. A script step
				// pinned by hash runs that deployed version, never the
				// stub pushed at its path (OF-14).
				if ps, ok := v.(*openflow.PathScript); ok && ps.Hash != nil {
					return nil, ErrStubUnsupported(fl.File, step, "the script step is pinned by hash, which selects a deployed version of the script rather than the stub pushed at its path (OF-14)")
				}
				if _, err := folder.Target(p, m); err != nil {
					return nil, ErrStubUnsupported(fl.File, step, err.Error())
				}
			case *openflow.FlowScript:
				return nil, ErrStubUnsupported(fl.File, step, "a flowscript step refers to a stored script that the tree has no source for")
			case *openflow.AIAgent:
				return nil, ErrStubUnsupported(fl.File, step, "AI-agent steps are out of scope")
			}
		}
	}
	// On a failure, remove what this call created: dst and any parent
	// directory it had to make, nothing that existed before.
	fail := func(err error) (*StubReport, error) {
		_ = os.RemoveAll(created)
		return nil, err
	}
	if err := CopyTree(src, dst); err != nil {
		return fail(err)
	}
	rep := &StubReport{}
	for file, s := range stubs {
		if err := os.WriteFile(filepath.Join(dst, filepath.FromSlash(file)), []byte(s), 0o644); err != nil {
			return fail(err)
		}
		rep.Files = append(rep.Files, file)
	}
	sort.Strings(rep.Files)
	return rep, nil
}

// outsideTree refuses an output directory that would lie inside the tree
// it copies: the copy would walk into itself. It compares file identities,
// not spellings: dst is inside src when src is the same directory as an
// existing ancestor of dst, after symbolic links are resolved, so a path
// through a link, another letter case on a case-insensitive file system or
// any other alias of src is caught, and distinct directories on a
// case-sensitive one stay distinct. It returns the first component of dst
// that does not exist yet, the directory a failed copy removes.
func outsideTree(src, dst string) (created string, err error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return "", err
	}
	// Walk up to the nearest existing ancestor of dst, resolve it, and put
	// the missing components back.
	existing, missing := abs, []string{}
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
	// With links resolved, each parent of the resolved ancestor is the
	// directory its spelling names, so the walk up is the real one.
	base, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	for a := base; ; {
		info, err := os.Stat(a)
		if err != nil {
			return "", err
		}
		if os.SameFile(srcInfo, info) {
			return "", fmt.Errorf("oracle: stub output %s is inside the sync tree %s, which the copy would walk into", dst, src)
		}
		parent := filepath.Dir(a)
		if parent == a {
			break
		}
		a = parent
	}
	return filepath.Join(existing, missing[0]), nil
}

// stubbable returns why a script cannot be stubbed, or "".
func stubbable(lang openflow.Language, content string) string {
	switch {
	case lang != openflow.LangBun:
		return fmt.Sprintf("the script's language is %s; stub mode replaces Bun scripts only", lang)
	case syncfolder.HasNativeAnnotation(content):
		return "the script carries the native annotation, which the stub would drop (SY-V3)"
	}
	return ""
}

// CheckStubSignatures compares the schema of every script of the sync
// tree orig with the same script's in stubbed, after generate-metadata ran
// on stubbed: the CLI derives a schema from main's signature, so equal
// schemas confirm that the stub kept what Windmill's parser reads.
func CheckStubSignatures(orig, stubbed string) error {
	a, err := syncfolder.Open(orig)
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := syncfolder.Open(stubbed)
	if err != nil {
		return err
	}
	defer b.Close()
	for _, p := range a.Paths(syncfolder.KindScript) {
		sa, err := a.Script(p)
		if err != nil {
			return err
		}
		sb, err := b.Script(p)
		if err != nil {
			return err
		}
		if !sameJSON(sa.Metadata.Schema, sb.Metadata.Schema) {
			return refusal("stub_signature_changed", "script", jsonString(p), "original", compactOrNull(sa.Metadata.Schema), "stub", compactOrNull(sb.Metadata.Schema))
		}
	}
	return nil
}

// sameJSON reports whether two JSON texts hold equal values, member order
// aside.
func sameJSON(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// compactOrNull returns v as compact JSON text, or null.
func compactOrNull(v json.RawMessage) string {
	var b bytes.Buffer
	if v == nil || json.Compact(&b, v) != nil {
		return "null"
	}
	return b.String()
}
