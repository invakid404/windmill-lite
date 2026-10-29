package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultStorage is the storage name of an object that names none: the
// workspace's default storage, as the stock client's presigned URLs write
// it (WA-16).
const DefaultStorage = "_default_"

// Object is a file of the object store: its content, and the content type
// and disposition its upload gave, "" when it gave none.
type Object struct {
	Content            []byte
	ContentType        string
	ContentDisposition string
}

// ObjectStore is the object store service of ADR 0007 (WA-16): files kept
// by storage and key, which the job helpers upload and download. Storage
// is DefaultStorage for an object that names none. It holds what the
// verified client contract needs (explicit keys); the keys a server
// generates, a resource's own bucket, deletion, quotas and presigning are
// `later` (WA-16, Q-WA-3).
type ObjectStore interface {
	// Put stores o at (storage, key), replacing what was there.
	Put(ctx context.Context, storage, key string, o Object) error
	// Get returns the object at (storage, key), and false when there is
	// none.
	Get(ctx context.Context, storage, key string) (Object, bool, error)
}

// DirObjects is the local default of the object store: a directory that
// keeps each object as one file, named by the SHA-256 of its storage and
// key, so that no key can name a path outside the directory, whatever its
// slashes or dots. The file holds one version of the object whole: a line
// of JSON metadata (its storage, key, content type and disposition), then
// its content. A Put writes the new version to a temporary file and
// renames it into place, so concurrent readers and writers only ever see
// a version some Put wrote, the last rename wins, and a Put that fails
// leaves the version before it as it was. It claims no parity with a CE
// server for what the client contract leaves open (WA-16's design note).
// It is safe for concurrent use.
type DirObjects struct {
	dir string
}

// NewDirObjects returns the object store kept in dir, which it creates
// when it does not exist.
func NewDirObjects(dir string) (*DirObjects, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("platform: the object store directory: %w", err)
	}
	return &DirObjects{dir: dir}, nil
}

// objectMeta is the metadata line of an object's file.
type objectMeta struct {
	Storage            string `json:"storage"`
	Key                string `json:"key"`
	ContentType        string `json:"content_type,omitempty"`
	ContentDisposition string `json:"content_disposition,omitempty"`
}

// path is the file of the object at (storage, key).
func (d *DirObjects) path(storage, key string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s:%s", len(storage), storage, key)
	return filepath.Join(d.dir, hex.EncodeToString(h.Sum(nil))+".obj")
}

// Put stores o as the object's one new version: its metadata line and its
// content in one file, written to a temporary file and renamed into place.
func (d *DirObjects) Put(_ context.Context, storage, key string, o Object) error {
	meta, err := json.Marshal(objectMeta{Storage: storage, Key: key, ContentType: o.ContentType, ContentDisposition: o.ContentDisposition})
	if err != nil {
		return err
	}
	data := make([]byte, 0, len(meta)+1+len(o.Content))
	data = append(append(append(data, meta...), '\n'), o.Content...)
	return writeAtomic(d.path(storage, key), data)
}

// Get returns the object at (storage, key): the one version its file
// holds, read whole.
func (d *DirObjects) Get(_ context.Context, storage, key string) (Object, bool, error) {
	raw, err := os.ReadFile(d.path(storage, key))
	if errors.Is(err, fs.ErrNotExist) {
		return Object{}, false, nil
	}
	if err != nil {
		return Object{}, false, err
	}
	line, content, found := bytes.Cut(raw, []byte{'\n'})
	var meta objectMeta
	if !found || json.Unmarshal(line, &meta) != nil || meta.Storage != storage || meta.Key != key {
		return Object{}, false, fmt.Errorf("platform: the file of object %s in storage %s is not one this store wrote", key, storage)
	}
	return Object{Content: content, ContentType: meta.ContentType, ContentDisposition: meta.ContentDisposition}, true, nil
}

// writeAtomic writes data to path through a temporary file in its
// directory, renamed into place.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
