package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The local object store keeps each object by (storage, key): another
// storage's object at the same key is another object, a Put replaces,
// metadata comes back, and a key whose slashes or dots would name a path
// outside the directory names a file inside it.
func TestDirObjects(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "objects")
	d, err := NewDirObjects(dir)
	if err != nil {
		t.Fatal(err)
	}
	put := func(storage, key string, o Object) {
		if err := d.Put(ctx, storage, key, o); err != nil {
			t.Fatal(err)
		}
	}
	put(DefaultStorage, "t/a.txt", Object{Content: []byte("hi"), ContentType: "text/plain"})
	put("other", "t/a.txt", Object{Content: []byte{0, 255}})
	put(DefaultStorage, "../../escape", Object{Content: []byte("x"), ContentDisposition: "attachment"})
	put("a:1", "b", Object{Content: []byte("one")})
	put("a", "1:b", Object{Content: []byte("two")})
	for _, c := range []struct {
		storage, key string
		want         Object
	}{
		{DefaultStorage, "t/a.txt", Object{Content: []byte("hi"), ContentType: "text/plain"}},
		{"other", "t/a.txt", Object{Content: []byte{0, 255}}},
		{DefaultStorage, "../../escape", Object{Content: []byte("x"), ContentDisposition: "attachment"}},
		{"a:1", "b", Object{Content: []byte("one")}},
		{"a", "1:b", Object{Content: []byte("two")}},
	} {
		got, ok, err := d.Get(ctx, c.storage, c.key)
		if err != nil || !ok || !bytes.Equal(got.Content, c.want.Content) || got.ContentType != c.want.ContentType || got.ContentDisposition != c.want.ContentDisposition {
			t.Errorf("(%s, %s): %+v %v %v", c.storage, c.key, got, ok, err)
		}
	}
	put(DefaultStorage, "t/a.txt", Object{Content: []byte("again")})
	if got, _, _ := d.Get(ctx, DefaultStorage, "t/a.txt"); string(got.Content) != "again" || got.ContentType != "" {
		t.Errorf("replaced: %+v", got)
	}
	if _, ok, err := d.Get(ctx, DefaultStorage, "none"); ok || err != nil {
		t.Errorf("a missing object: %v %v", ok, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("unexpected entry %s", e.Name())
		}
	}
	if len(entries) != 10 {
		t.Errorf("%d files for 5 objects", len(entries))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(err) {
		t.Errorf("a key escaped the directory: %v", err)
	}
}
