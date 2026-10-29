package oracle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// Layout is how a copy of a sync tree is laid out on disk. A codebase entry
// of wmill.yaml (SY-8) may name paths outside the tree: its relative_path,
// whose files the CLI digests into the codebase id (WR-49), its assets'
// from files, which the CLI uploads with the bundle (WR-49) and
// windmill-lite's build reads, and its inject files, which esbuild bundles
// (WR-47). Each resolves against the tree's root, the CLI's working
// directory. A copy must not rewrite them, because the CLI digests the
// entry's settings as written; it carries those paths instead, at the same
// places relative to the copied root. The tree is copied below as many of
// its own parent directories' names as the furthest of those paths climbs,
// and each path beside it, so that every path of the copy reaches a copy of
// what the original's reaches.
type Layout struct {
	// Root is the copied tree's directory, relative to the copy's
	// directory, in slash form: "." when no entry reaches outside the tree.
	Root string `json:"root"`
	// Outside are the paths outside the tree that the copy carries,
	// relative to the tree's root, in slash form and sorted: a directory
	// with everything below it, or a file.
	Outside []string `json:"outside,omitempty"`
}

// PlanLayout returns the layout of a copy of the sync tree at src, the
// directory of its wmill.yaml. Its outside paths are the relative_path,
// asset from and inject paths of the codebase entries that resolve, after
// "." and ".." are applied to the spelling, outside the tree. A path below
// another is carried with it, and one that does not exist is left out, as
// the original does not resolve it either; an absolute path resolves the
// same from a copy. An import of a codebase script that leaves the tree
// resolves in the copy when it lands below one of these paths, as an
// import of a shared library below the entry's relative_path does:
// esbuild follows imports wherever they lead (WR-47), and PlanLayout does
// not follow them. A wmill.yaml that is missing or does not parse names no
// paths; the CLI and the build report it themselves.
func PlanLayout(src string) (*Layout, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	l := &Layout{Root: "."}
	data, err := os.ReadFile(filepath.Join(abs, "wmill.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	} else if err != nil {
		return nil, err
	}
	doc, err := yamlx.Parse(data)
	if err != nil {
		return l, nil
	}
	var outside []string
	for _, p := range codebasePaths(doc) {
		p = filepath.FromSlash(p)
		if p == "" || filepath.IsAbs(p) {
			continue
		}
		rel, err := filepath.Rel(abs, filepath.Join(abs, p))
		if err != nil {
			return nil, err
		}
		if climbs(rel) == 0 {
			continue
		}
		if _, err := os.Lstat(filepath.Join(abs, rel)); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		outside = append(outside, filepath.ToSlash(rel))
	}
	sort.Strings(outside)
	up := 0
	for _, o := range outside {
		carried := false
		for _, k := range outside {
			if k != o && strings.HasPrefix(o, k+"/") {
				carried = true
				break
			}
		}
		if carried || (len(l.Outside) > 0 && l.Outside[len(l.Outside)-1] == o) {
			continue
		}
		l.Outside = append(l.Outside, o)
		up = max(up, climbs(filepath.FromSlash(o)))
	}
	var names []string
	for d := abs; len(names) < up; d = filepath.Dir(d) {
		if filepath.Dir(d) == d {
			return nil, fmt.Errorf("oracle: a codebase path of %s climbs above the file system root", src)
		}
		names = append([]string{filepath.Base(d)}, names...)
	}
	if up > 0 {
		l.Root = path.Join(names...)
	}
	return l, nil
}

// codebasePaths returns the paths the codebases entries of wmill.yaml name
// as they are written: each entry's relative_path, every asset's from and
// every inject file. A value of another type than the field takes is
// skipped; the build reports it.
func codebasePaths(doc *yamlx.Node) []string {
	entries := doc.Get("codebases")
	if entries == nil || entries.Kind != yamlx.Sequence {
		return nil
	}
	var out []string
	str := func(n *yamlx.Node) {
		if n != nil && n.Kind == yamlx.String {
			out = append(out, n.Text)
		}
	}
	for _, e := range entries.Items {
		str(e.Get("relative_path"))
		if assets := e.Get("assets"); assets != nil && assets.Kind == yamlx.Sequence {
			for _, a := range assets.Items {
				str(a.Get("from"))
			}
		}
		if inject := e.Get("inject"); inject != nil && inject.Kind == yamlx.Sequence {
			for _, i := range inject.Items {
				str(i)
			}
		}
	}
	return out
}

// climbs returns how many leading ".." components a clean relative path
// has: 0 for a path inside the directory it is relative to.
func climbs(rel string) int {
	n := 0
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c != ".." {
			break
		}
		n++
	}
	return n
}

// target checks that dst can take a copy of the tree at src laid out as l:
// dst must not exist, and must not lie inside the tree or inside a path the
// copy carries, which the copy would walk into (outsideTree). It returns
// the first component of dst that does not exist yet, the directory a
// failed copy removes.
func (l *Layout) target(src, dst string) (created string, err error) {
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("oracle: output %s already exists", dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if created, err = outsideTree(src, dst); err != nil {
		return "", err
	}
	for _, o := range l.Outside {
		carried := filepath.Join(src, filepath.FromSlash(o))
		if _, err := outsideOf(carried, carried+", which the copy carries from outside the sync tree", dst); err != nil {
			return "", err
		}
	}
	return created, nil
}

// copy writes the copy of the tree at src into dst: each outside path at
// its place relative to the copied root, and then the tree at dst/Root. A
// carried directory that holds the tree (relative_path ..) writes the
// tree's files too, and the tree's own copy writes the same files again.
func (l *Layout) copy(src, dst string) error {
	root := filepath.Join(dst, filepath.FromSlash(l.Root))
	for _, o := range l.Outside {
		o = filepath.FromSlash(o)
		if err := copyPath(filepath.Join(src, o), filepath.Join(root, o)); err != nil {
			return err
		}
	}
	return copyPath(src, root)
}

// CopyLayout copies the sync tree at src into dst as PlanLayout lays it
// out, and returns the layout: the copied tree is dst/Root. dst must not
// exist, and must not lie inside the tree or a path the copy carries. A
// copy that fails removes dst and any parent directory it made.
func CopyLayout(src, dst string) (*Layout, error) {
	l, err := PlanLayout(src)
	if err != nil {
		return nil, err
	}
	created, err := l.target(src, dst)
	if err != nil {
		return nil, err
	}
	if err := l.copy(src, dst); err != nil {
		_ = os.RemoveAll(created)
		return nil, err
	}
	return l, nil
}
