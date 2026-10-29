package oracle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

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
// its own parent directories' names as the furthest of those paths climbs
// as it is spelled, and each path beside it, so that every path of the copy
// reaches a copy of what the original's reaches, whether "." and ".." are
// applied to the spelling first or walked through on disk.
type Layout struct {
	// Root is the copied tree's directory, relative to the copy's
	// directory, in slash form: "." when no entry's path climbs out of the
	// tree.
	Root string `json:"root"`
	// Outside are the paths outside the tree that the copy carries,
	// relative to the tree's root, in slash form and sorted: a directory
	// with everything below it, or a file.
	Outside []string `json:"outside,omitempty"`
	// Dirs are the directories outside the tree, and not carried, that a
	// path's spelling passes through on its way (the detour of
	// ../detour/../shared/a.txt), relative to the tree's root, in slash
	// form and sorted: the deepest of them, since making a directory makes
	// its parents. The copy makes each one, empty, so that the spelling
	// resolves when it is walked on disk.
	Dirs []string `json:"dirs,omitempty"`
}

// PlanLayout returns the layout of a copy of the sync tree at src, the
// directory of its wmill.yaml, from the relative_path, asset from and
// inject paths of the codebase entries. Each path is walked as it is
// spelled: the furthest it climbs above the tree sets Root, even when it
// comes back in (../windmill/f from a tree named windmill), the
// directories it passes through outside the tree that exist in the
// original are Dirs, and its target, "." and ".." applied, is carried when
// it lies outside the tree. A target below another is carried with it, and
// a path whose target does not exist is left out, as the original does
// not resolve it either; an absolute path resolves the same from a copy. A
// path that climbs above the file system root cannot be laid out inside
// the copy and is refused. An import of a codebase script that leaves the tree
// resolves in the copy when it lands below one of these paths, as an
// import of a shared library below the entry's relative_path does:
// esbuild follows imports wherever they lead (WR-47), and PlanLayout does
// not follow them. A wmill.yaml that is missing or does not parse names no
// paths; the CLI and the build report it themselves. The paths are taken
// from src with its symbolic links resolved, as the CLI's working
// directory has them, so ".." climbs from the directory src names.
func PlanLayout(src string) (*Layout, error) {
	abs, err := realDir(src)
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
	var outside, through []string
	up := 0
	for _, p := range codebasePaths(doc) {
		p = filepath.FromSlash(p)
		if p == "" || filepath.IsAbs(p) {
			continue
		}
		rel, err := filepath.Rel(abs, filepath.Join(abs, p))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(abs, rel)); missing(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		peak, dirs := walkSpelling(p)
		up = max(up, peak)
		for _, d := range dirs {
			// Spelled through the tree's own name or its parents' names,
			// a directory may be the tree, inside it, or one of the
			// parents the copy has anyway.
			if d, err = filepath.Rel(abs, filepath.Join(abs, d)); err != nil {
				return nil, err
			}
			if n := climbs(d); n == 0 || n == len(strings.Split(d, string(filepath.Separator))) {
				continue
			}
			if info, err := os.Stat(filepath.Join(abs, d)); err == nil && info.IsDir() {
				through = append(through, filepath.ToSlash(d))
			} else if err != nil && !missing(err) {
				return nil, err
			}
		}
		if climbs(rel) > 0 {
			outside = append(outside, filepath.ToSlash(rel))
		}
	}
	l.Outside = outermost(outside)
	l.Dirs = deepest(through, l.Outside)
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

// walkSpelling walks a relative path as it is spelled, one component at a
// time from the tree's root, and returns the furthest it climbs above the
// root and the directories it passes through on the way while above the
// root, as clean paths relative to the root: for
// ../detour/../shared/a.txt, 1 and [../detour].
func walkSpelling(p string) (peak int, dirs []string) {
	parts := strings.Split(p, string(filepath.Separator))
	up, down := 0, []string{}
	for i, c := range parts {
		switch c {
		case "", ".":
			continue
		case "..":
			if len(down) > 0 {
				down = down[:len(down)-1]
			} else {
				up++
				peak = max(peak, up)
			}
		default:
			down = append(down, c)
		}
		if i < len(parts)-1 && up > 0 && len(down) > 0 {
			dirs = append(dirs, filepath.Join(append(slices.Repeat([]string{".."}, up), down...)...))
		}
	}
	return peak, dirs
}

// outermost returns the carried paths of list that are not below another
// of list, sorted and once each: copying a directory copies everything
// below it.
func outermost(list []string) []string {
	sort.Strings(list)
	var out []string
	for i, o := range list {
		if i > 0 && list[i-1] == o || slices.ContainsFunc(list, func(k string) bool { return below(o, k) }) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// deepest returns the directories of list that the copy must make, sorted
// and once each: those that no other of list lies below, since making a
// directory makes its parents, and that no carried path supplies, by
// being it, holding it or lying below it.
func deepest(list, carried []string) []string {
	sort.Strings(list)
	var out []string
	for i, o := range list {
		if i > 0 && list[i-1] == o || slices.ContainsFunc(list, func(k string) bool { return below(k, o) }) {
			continue
		}
		if slices.ContainsFunc(carried, func(k string) bool { return k == o || below(o, k) || below(k, o) }) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// below reports whether the slash path o lies below k: it starts with k's
// components and climbs no further, so ../../x is not below .., though it
// starts with "../".
func below(o, k string) bool {
	return strings.HasPrefix(o, k+"/") && climbs(filepath.FromSlash(o)) == climbs(filepath.FromSlash(k))
}

// missing reports whether a Stat error says the path does not resolve: it
// does not exist, or a component of it is a file.
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// realDir returns the absolute path of the directory dir with its symbolic
// links resolved.
func realDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
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
	real, err := realDir(src)
	if err != nil {
		return "", err
	}
	for _, o := range l.Outside {
		carried := filepath.Join(real, filepath.FromSlash(o))
		if _, err := outsideOf(carried, "the carried path "+carried, dst); err != nil {
			return "", err
		}
	}
	return created, nil
}

// copy writes the copy of the tree at src, links in its path resolved,
// into dst: each outside path at its place relative to the copied root,
// then the tree at dst/Root, then each of Dirs, empty. A carried directory
// that holds the tree (relative_path ..) writes the tree's files too, and
// the tree's own copy writes the same files again.
func (l *Layout) copy(src, dst string) error {
	real, err := realDir(src)
	if err != nil {
		return err
	}
	root := filepath.Join(dst, filepath.FromSlash(l.Root))
	for _, o := range l.Outside {
		o = filepath.FromSlash(o)
		if err := copyPath(filepath.Join(real, o), filepath.Join(root, o)); err != nil {
			return err
		}
	}
	if err := copyPath(real, root); err != nil {
		return err
	}
	for _, d := range l.Dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			return err
		}
	}
	return nil
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
