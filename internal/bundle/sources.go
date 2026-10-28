package bundle

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"
)

// sourceFS is the sync folder as the build reads it: it records the
// digest of every file read through it, which becomes the manifest's
// source inventory. The loader reads files with fs.ReadFile; a file opened
// with Open is recorded too, and read again for its digest at the end.
type sourceFS struct {
	fsys fs.FS

	mu     sync.Mutex
	paused bool
	read   map[string]string // path → digest
	opened map[string]bool
}

func newSourceFS(fsys fs.FS) *sourceFS {
	return &sourceFS{fsys: fsys, read: map[string]string{}, opened: map[string]bool{}}
}

func (s *sourceFS) Open(name string) (fs.File, error) {
	f, err := s.fsys.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.Mode().IsRegular() {
		s.mu.Lock()
		if !s.paused {
			s.opened[name] = true
		}
		s.mu.Unlock()
	}
	return f, nil
}

func (s *sourceFS) ReadFile(name string) ([]byte, error) {
	data, err := fs.ReadFile(s.fsys, name)
	if err == nil {
		s.mu.Lock()
		if !s.paused {
			s.read[name] = Digest(data)
		}
		s.mu.Unlock()
	}
	return data, err
}

func (s *sourceFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(s.fsys, name) }

func (s *sourceFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(s.fsys, name) }

// pause stops recording: the reads that follow are not inputs of the
// bundle.
func (s *sourceFS) pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

// add records a file the build read by other means (the Bun helper reads
// script files itself).
func (s *sourceFS) add(name string) error {
	data, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.read[name] = Digest(data)
	s.mu.Unlock()
	return nil
}

// forget drops files from the record: inputs whose digest the manifest
// must not carry.
func (s *sourceFS) forget(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		delete(s.read, n)
		delete(s.opened, n)
	}
}

// inventory returns the digest of every file recorded.
func (s *sourceFS) inventory() (map[string]string, error) {
	s.mu.Lock()
	var pending []string
	for name := range s.opened {
		if _, ok := s.read[name]; !ok {
			pending = append(pending, name)
		}
	}
	s.mu.Unlock()
	for _, name := range pending {
		if err := s.add(name); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.read))
	for k, v := range s.read {
		out[k] = v
	}
	return out, nil
}

// TreeDigest is the digest of a directory tree: the Digest of the
// canonical JSON object that maps the slash path of every regular file
// under dir to its Digest. Directories named node_modules, and files and
// directories whose name starts with "." (.DS_Store, an editor's swap
// file), are left out. Anything else below dir that is neither a regular
// file nor a directory, a symbolic link above all, is refused: a link
// would let what the runtime loads change without the digest. dir itself
// may be a link. The manifest records the harness runtime's tree digest.
func TreeDigest(dir string) (string, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	files := map[string]string{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if d.Name() == "node_modules" || d.Name()[0] == '.' {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			// Bun loads through a link as through the file, so a digest
			// that skipped it would not change with what runs.
			rel, _ := filepath.Rel(dir, p)
			return fmt.Errorf("bundle: %s: %s is a symbolic link or another file that is not regular; a harness runtime must hold its files itself", dir, filepath.ToSlash(rel))
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = Digest(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("bundle: %s holds no files", dir)
	}
	b, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	canon, err := Canonicalize(b)
	if err != nil {
		return "", err
	}
	return Digest(canon), nil
}

// cleanRel reports whether p is a clean relative slash path that stays in
// its root.
func cleanRel(p string) bool {
	return p != "" && path.Clean(p) == p && fs.ValidPath(p) && p != "."
}
