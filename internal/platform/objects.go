package platform

import (
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
// keeps each object as two files, its content and its metadata, named by
// the SHA-256 of its storage and key, so that no key can name a path
// outside the directory, whatever its slashes or dots. It claims no parity
// with a CE server for what the client contract leaves open (WA-16's
// design note). It is safe for concurrent use: each file is replaced
// whole, so a reader never sees a partly written content.
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

// objectMeta is what an object's metadata file holds.
type objectMeta struct {
	Storage            string `json:"storage"`
	Key                string `json:"key"`
	ContentType        string `json:"content_type,omitempty"`
	ContentDisposition string `json:"content_disposition,omitempty"`
}

// name is the file name of the object at (storage, key), without its
// extension.
func (d *DirObjects) name(storage, key string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s:%s", len(storage), storage, key)
	return filepath.Join(d.dir, hex.EncodeToString(h.Sum(nil)))
}

// Put stores o, its content first and then its metadata, each by writing
// a temporary file and renaming it into place.
func (d *DirObjects) Put(_ context.Context, storage, key string, o Object) error {
	base := d.name(storage, key)
	meta, err := json.Marshal(objectMeta{Storage: storage, Key: key, ContentType: o.ContentType, ContentDisposition: o.ContentDisposition})
	if err != nil {
		return err
	}
	if err := writeAtomic(base+".data", o.Content); err != nil {
		return err
	}
	return writeAtomic(base+".json", meta)
}

// Get returns the object at (storage, key).
func (d *DirObjects) Get(_ context.Context, storage, key string) (Object, bool, error) {
	base := d.name(storage, key)
	raw, err := os.ReadFile(base + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return Object{}, false, nil
	}
	if err != nil {
		return Object{}, false, err
	}
	var meta objectMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Object{}, false, fmt.Errorf("platform: the metadata of object %s in storage %s: %w", key, storage, err)
	}
	content, err := os.ReadFile(base + ".data")
	if err != nil {
		return Object{}, false, err
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
