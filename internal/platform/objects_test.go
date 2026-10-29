package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	if len(entries) != 5 {
		t.Errorf("%d files for 5 objects", len(entries))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(err) {
		t.Errorf("a key escaped the directory: %v", err)
	}
}

// pf3 review round 1, P2-2: concurrent writers and readers of one object
// only ever see a version some Put wrote, its content and metadata
// together, and once the writers are done the object is the last version
// one of them wrote, whole.
func TestDirObjectsConcurrentVersions(t *testing.T) {
	ctx := context.Background()
	d, err := NewDirObjects(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	version := func(v string) Object {
		return Object{Content: []byte(strings.Repeat(v, 1+len(v)%7)), ContentType: v, ContentDisposition: "attachment; " + v}
	}
	coherent := func(o Object) bool { return reflect.DeepEqual(o, version(o.ContentType)) }
	if err := d.Put(ctx, DefaultStorage, "k", version("initial")); err != nil {
		t.Fatal(err)
	}
	const writers, puts = 4, 100
	last := make([]string, writers)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range puts {
				v := fmt.Sprintf("w%d-%d", w, i)
				if err := d.Put(ctx, DefaultStorage, "k", version(v)); err != nil {
					t.Error(err)
					return
				}
				last[w] = v
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	reads := 0
	for running := true; running; reads++ {
		select {
		case <-done:
			running = false
		default:
		}
		o, ok, err := d.Get(ctx, DefaultStorage, "k")
		if err != nil || !ok || !coherent(o) {
			t.Fatalf("read %d: a mixed or missing version: %q %q %q, %v %v", reads, o.Content, o.ContentType, o.ContentDisposition, ok, err)
		}
	}
	o, _, _ := d.Get(ctx, DefaultStorage, "k")
	if !coherent(o) || !slices.Contains(last, o.ContentType) {
		t.Errorf("the final version %q is not a writer's last (%q)", o.ContentType, last)
	}
}

// A Put that fails leaves the version before it whole, and no temporary
// file behind: here the directory refuses the new file.
func TestDirObjectsFailedPut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	ctx := context.Background()
	dir := t.TempDir()
	d, err := NewDirObjects(dir)
	if err != nil {
		t.Fatal(err)
	}
	committed := Object{Content: []byte("committed"), ContentType: "text/plain"}
	if err := d.Put(ctx, DefaultStorage, "k", committed); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if err := d.Put(ctx, DefaultStorage, "k", Object{Content: []byte("replacement")}); err == nil {
		t.Fatal("a Put into a read-only directory succeeded")
	}
	if o, ok, err := d.Get(ctx, DefaultStorage, "k"); err != nil || !ok || !reflect.DeepEqual(o, committed) {
		t.Errorf("after the failed Put: %+v %v %v", o, ok, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d files after the failed Put", len(entries))
	}
}

// A file the store did not write, or another object's, is refused rather
// than read as the object.
func TestDirObjectsForeignFile(t *testing.T) {
	ctx := context.Background()
	d, err := NewDirObjects(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, DefaultStorage, "a", Object{Content: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(d.path(DefaultStorage, "a"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"b": raw, "c": []byte("no metadata line")} {
		if err := os.WriteFile(d.path(DefaultStorage, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.Get(ctx, DefaultStorage, name); err == nil {
			t.Errorf("%s: a foreign file was read as the object", name)
		}
	}
}
