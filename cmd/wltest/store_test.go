package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/wl"
)

// TestFixtureStoresAreApart: with -resource-store redis, two fixtures'
// stores (fixtureStore, which each fixture's runtime gets) on one Redis
// keep apart the same workspace and path, so a fixture starts with no
// state, as on the memory store; one fixture's store is shared by its runs.
// It needs WL_TEST_REDIS_URL (wl/redisstore's tests), and skips without it.
func TestFixtureStoresAreApart(t *testing.T) {
	url := os.Getenv("WL_TEST_REDIS_URL")
	if url == "" {
		t.Skip("WL_TEST_REDIS_URL is not set; scripts/redis-test-env.sh starts the servers and prints it")
	}
	ctx := context.Background()
	fc := &flowChecker{redisURL: url}
	first, err := fc.fixtureStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := fc.fixtureStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	const ws, path = "wltest-ws", "f/wltest/state"
	r := wl.Resource{Type: "state", Value: json.RawMessage(`{"n":1}`)}
	if ok, err := first.Create(ctx, ws, path, r); err != nil || !ok {
		t.Fatalf("the first fixture's create = %v, %v", ok, err)
	}
	if got, ok, err := first.Get(ctx, ws, path); err != nil || !ok || string(got.Value) != `{"n":1}` {
		t.Errorf("the first fixture reads %v %v %v, want its own state", got, ok, err)
	}
	if _, ok, err := second.Get(ctx, ws, path); err != nil || ok {
		t.Errorf("the second fixture reads %v, %v, want no state", ok, err)
	}
	if ok, err := second.Create(ctx, ws, path, r); err != nil || !ok {
		t.Errorf("the second fixture's create = %v, %v, want true", ok, err)
	}
	for _, s := range []interface {
		Delete(context.Context, string, string, string) (bool, error)
	}{first, second} {
		if ok, err := s.Delete(ctx, ws, path, "state"); err != nil || !ok {
			t.Errorf("cleanup: Delete = %v, %v", ok, err)
		}
	}
}

// TestFixtureStateOnRedis runs the fixture testdata/state-isolation twice
// through the checker with -resource-store redis, as wltest check does: in
// each, the second run reads the counter the first wrote, so the runs of
// one fixture share its store; the second fixture starts at zero, so each
// fixture gets a store of its own; and Redis holds two counters at 2, one
// per fixture's prefix, so the fixtures' runtimes used the Redis store, not
// a memory one. It needs WL_TEST_REDIS_URL and the pinned Bun, and skips
// without them.
func TestFixtureStateOnRedis(t *testing.T) {
	url := os.Getenv("WL_TEST_REDIS_URL")
	if url == "" {
		t.Skip("WL_TEST_REDIS_URL is not set; scripts/redis-test-env.sh starts the servers and prints it")
	}
	bun := bunOrSkip(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	o, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal("WL_TEST_REDIS_URL does not parse")
	}
	c := redis.NewClient(o)
	// Registered first, so it runs after the cleanup below.
	t.Cleanup(func() { c.Close() })
	ctx := context.Background()
	const pattern = "wltest-*:res:*:f/wlt_iso/state"
	keys := func() []string {
		t.Helper()
		var found []string
		iter := c.Scan(ctx, 0, pattern, 100).Iterator()
		for iter.Next(ctx) {
			found = append(found, iter.Val())
		}
		if err := iter.Err(); err != nil {
			t.Fatal(err)
		}
		return found
	}
	clean := func() {
		for _, k := range keys() {
			c.Del(ctx, k)
		}
	}
	clean()
	t.Cleanup(clean)

	dir := filepath.Join("testdata", "state-isolation")
	fc := &flowChecker{bun: bun, repo: root, mode: conformance.ModeWarm, js: js, redisURL: url}
	frs := fc.runFixtures(ctx, []string{dir, dir})
	if len(frs) != 2 {
		t.Fatalf("%d fixture results, want 2", len(frs))
	}
	for i, fr := range frs {
		if fr.err != nil || len(fr.runs) != 2 {
			t.Fatalf("fixture %d: %v, %d runs", i, fr.err, len(fr.runs))
		}
		for _, r := range fr.runs {
			if r.unsupported != "" || len(r.problems) > 0 {
				t.Errorf("fixture %d, run %s: %s %s\n%s", i, r.run, r.unsupported, strings.Join(r.problems, "; "), r.diag)
			}
		}
	}
	found := keys()
	if len(found) != 2 {
		t.Fatalf("Redis holds %d counters, want 2, one per fixture: %v", len(found), found)
	}
	for _, k := range found {
		if v, err := c.HGet(ctx, k, "v").Result(); err != nil || v != "2" {
			t.Errorf("%s holds %q, %v, want 2", k, v, err)
		}
	}
}
