package bundle

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ArchiveExt is the extension of a bundle archive: the bundle tree as one
// tar file (ADR 0010).
const ArchiveExt = ".wlb"

// WriteArchive writes the bundle directory dir as a .wlb archive to w: a
// tar file whose first entry is manifest.json, followed by every file the
// manifest lists, in byte order of their paths. Every header is fixed
// (mode 0644, owner 0, modification time 0, PAX format), so one bundle
// gives one archive. A file whose content no longer matches the manifest
// fails the write.
func WriteArchive(dir string, w io.Writer) error {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return err
	}
	m, _, err := ParseManifest(data)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	if err := writeEntry(tw, ManifestFile, data); err != nil {
		return err
	}
	paths := make([]string, 0, len(m.Files))
	for p := range m.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		if Digest(b) != m.Files[p] {
			return fmt.Errorf("bundle: %s does not match the manifest", p)
		}
		if err := writeEntry(tw, p, b); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(data)),
		ModTime:  time.Unix(0, 0),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// WriteArchiveFile writes the bundle directory dir as the archive file
// path, through a temporary file renamed into place.
func WriteArchiveFile(dir, path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := WriteArchive(dir, f); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Verification is what Verify found.
type Verification struct {
	// Digest is the bundle digest, Manifest the artifact's manifest.
	Digest   string
	Manifest *Manifest
}

// VerifyOptions name what an artifact must match beyond its own manifest.
type VerifyOptions struct {
	// Release is a release manifest: the artifact's files are checked
	// against it, and its manifest must be the same bytes.
	Release []byte
	// Digest is the bundle digest the artifact must have.
	Digest string
}

// MismatchError lists every way an artifact differs from the manifest it
// was checked against.
type MismatchError struct {
	Problems []string
}

func (e *MismatchError) Error() string {
	return "bundle: the artifact does not match its manifest:\n  " + strings.Join(e.Problems, "\n  ")
}

// Verify recomputes the digest of every file of a bundle, a directory or a
// .wlb archive, and checks them against a manifest: the release manifest
// when opts gives one, otherwise the artifact's own (ADR 0010, drift guard
// 3). The artifact must hold exactly the files the manifest lists, as
// regular files. Differences come back as a *MismatchError.
func Verify(artifact string, opts VerifyOptions) (*Verification, error) {
	fi, err := os.Stat(artifact)
	if err != nil {
		return nil, err
	}
	var manifest []byte
	var found map[string]string
	if fi.IsDir() {
		manifest, found, err = readTree(artifact)
	} else {
		manifest, found, err = readArchive(artifact)
	}
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, fmt.Errorf("bundle: %s has no %s", artifact, ManifestFile)
	}
	own, digest, err := ParseManifest(manifest)
	if err != nil {
		return nil, err
	}
	want := own
	var problems []string
	if opts.Release != nil {
		rel, relDigest, err := ParseManifest(opts.Release)
		if err != nil {
			return nil, fmt.Errorf("the release manifest: %w", err)
		}
		if relDigest != digest {
			problems = append(problems, fmt.Sprintf("%s: digest %s, the release manifest's is %s", ManifestFile, digest, relDigest))
		}
		want = rel
	}
	if opts.Digest != "" && opts.Digest != digest {
		problems = append(problems, fmt.Sprintf("bundle digest %s, want %s", digest, opts.Digest))
	}
	paths := make([]string, 0, len(want.Files)+len(found))
	for p := range want.Files {
		paths = append(paths, p)
	}
	for p := range found {
		if _, ok := want.Files[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		w, listed := want.Files[p]
		got, present := found[p]
		switch {
		case !present:
			problems = append(problems, p+": missing")
		case !listed:
			problems = append(problems, p+": not in the manifest")
		case got != w:
			problems = append(problems, fmt.Sprintf("%s: digest %s, the manifest says %s", p, got, w))
		}
	}
	if len(problems) > 0 {
		return nil, &MismatchError{Problems: problems}
	}
	return &Verification{Digest: digest, Manifest: own}, nil
}

// readTree reads a bundle directory: the manifest's bytes and the digest
// of every other file.
func readTree(dir string) ([]byte, map[string]string, error) {
	// The walk does not follow a symbolic link, the artifact's own path
	// included.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, nil, err
	}
	var manifest []byte
	found := map[string]string{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fmt.Errorf("bundle: %s is not a regular file", rel)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == ManifestFile {
			manifest = data
		} else {
			found[rel] = Digest(data)
		}
		return nil
	})
	return manifest, found, err
}

// maxManifest bounds the manifest read from an archive.
const maxManifest = 64 << 20

// readArchive reads a .wlb archive: the manifest's bytes and the digest of
// every other entry. Entries must be regular files (directories are
// skipped) with clean relative names, each named once.
func readArchive(path string) ([]byte, map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var manifest []byte
	found := map[string]string{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("bundle: %s: %w", path, err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || !cleanRel(name) {
			return nil, nil, fmt.Errorf("bundle: %s: entry %q is not a regular file with a clean relative name", path, hdr.Name)
		}
		if _, dup := found[name]; dup || (name == ManifestFile && manifest != nil) {
			return nil, nil, fmt.Errorf("bundle: %s: entry %q appears twice", path, name)
		}
		if name == ManifestFile {
			if manifest, err = io.ReadAll(io.LimitReader(tr, maxManifest+1)); err != nil {
				return nil, nil, err
			}
			if len(manifest) > maxManifest {
				return nil, nil, fmt.Errorf("bundle: %s: the manifest is larger than %d bytes", path, maxManifest)
			}
			continue
		}
		h := sha256.New()
		if _, err := io.Copy(h, tr); err != nil {
			return nil, nil, err
		}
		found[name] = "sha256:" + hex.EncodeToString(h.Sum(nil))
	}
	return manifest, found, nil
}
