package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

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
