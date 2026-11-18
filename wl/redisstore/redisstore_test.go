package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/storetest"
)

// The tests that need Redis read its URLs from the environment, which
// scripts/redis-test-env.sh sets up, and skip without them:
//
//   - WL_TEST_REDIS_URL: one server, which they may reconfigure
//     (maxmemory-policy, an ACL user) and restore.
//   - WL_TEST_REDIS_SENTINEL_URL: a Sentinel URL of that server's master.
//   - WL_TEST_TOXIPROXY: the address of a Toxiproxy API that reaches the
//     server (fault_test.go).
const (
	urlEnv      = "WL_TEST_REDIS_URL"
	sentinelEnv = "WL_TEST_REDIS_SENTINEL_URL"
)

// envOrSkip returns the environment variable name, or skips t without it.
func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set; scripts/redis-test-env.sh starts the servers and prints it", name)
	}
	return v
}

// direct returns a go-redis client of the server WL_TEST_REDIS_URL names,
// for checks that go around the store.
func direct(t *testing.T) *redis.Client {
	t.Helper()
	o, err := redis.ParseURL(envOrSkip(t, urlEnv))
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(o)
	t.Cleanup(func() { c.Close() })
	return c
}

// testPrefix returns a key prefix of t's own, whose keys are deleted when
// t ends.
func testPrefix(t *testing.T, c *redis.Client) string {
	t.Helper()
	prefix := "wl-test:" + writeID()[:12] + ":"
	t.Cleanup(func() {
		ctx := context.Background()
		iter := c.Scan(ctx, 0, prefix+"*", 100).Iterator()
		for iter.Next(ctx) {
			c.Del(ctx, iter.Val())
		}
	})
	return prefix
}

// open returns a store for url and prefix, closed when t ends, and the
// warnings New gave.
func open(t *testing.T, o Options) (*Store, []string) {
	t.Helper()
	var mu sync.Mutex
	var warnings []string
	o.Warn = func(m string) {
		mu.Lock()
		defer mu.Unlock()
		warnings = append(warnings, m)
	}
	s, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, warnings
}

// TestEvicts: allkeys-* evicts the store's entries, noeviction and
// volatile-* keep them, and any other name is unknown.
func TestEvicts(t *testing.T) {
	for policy, want := range map[string]Eviction{
		"noeviction": KeepsEntries, "volatile-lru": KeepsEntries, "volatile-lfu": KeepsEntries,
		"volatile-random": KeepsEntries, "volatile-ttl": KeepsEntries,
		"allkeys-lru": EvictsEntries, "allkeys-lfu": EvictsEntries, "allkeys-random": EvictsEntries,
		"": UnknownPolicy, "lru": UnknownPolicy, "NOEVICTION": UnknownPolicy,
	} {
		if got := Evicts(policy); got != want {
			t.Errorf("Evicts(%q) = %d, want %d", policy, got, want)
		}
	}
}

// TestURLErrorsHideThePassword: a URL that does not parse, or that no
// server answers, gives an error that never holds its password.
func TestURLErrorsHideThePassword(t *testing.T) {
	const secret = "s3cr3t-pw"
	for _, u := range []string{
		"redis://user:" + secret + "@host:notaport/0",
		"redis://user:" + secret + "@host:6379/x",
		"redis://user:" + secret + "@host:6379/0?bogus=1",
		"ftp://user:" + secret + "@host:6379",
		"redis://user:" + secret + "%zz@host:6379",
		"redis://user:" + secret + "@ho st:6379",
		"redis://host:6379/0?master_name=m&password=" + secret + "&bogus=1",
		"redis://user:" + secret + "@host:6379/0?master_name=m&addr=bad:addr:x",
		"",
	} {
		s, err := New(context.Background(), Options{URL: u, OperationTimeout: time.Second})
		if err == nil {
			s.Close()
			t.Errorf("New(%q) succeeded", u)
			continue
		}
		if strings.Contains(err.Error(), secret) || !strings.HasPrefix(err.Error(), "redisstore: ") {
			t.Errorf("New(%q): %v", u, err)
		}
	}
	// No server listens on port 1: New fails within its timeout, naming
	// the address and not the password.
	start := time.Now()
	_, err := New(context.Background(), Options{URL: "redis://user:" + secret + "@127.0.0.1:1/2", OperationTimeout: 2 * time.Second})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "cannot reach Redis at 127.0.0.1:1 (database 2)") {
		t.Errorf("an unreachable server: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("an unreachable server: New took %v", d)
	}
}

// TestColonInWorkspace: a workspace id with a colon is refused by every
// operation, before any command: its key could name another workspace's
// path.
func TestColonInWorkspace(t *testing.T) {
	s := &Store{prefix: DefaultPrefix}
	ctx := context.Background()
	want := `redisstore: the workspace id "a:b" contains a colon`
	r := wl.Resource{Type: "state", Value: json.RawMessage(`1`)}
	for name, err := range map[string]error{
		"Get":         func() error { _, _, err := s.Get(ctx, "a:b", "c"); return err }(),
		"Create":      func() error { _, err := s.Create(ctx, "a:b", "c", r); return err }(),
		"UpdateValue": func() error { _, err := s.UpdateValue(ctx, "a:b", "c", "state", r.Value); return err }(),
		"Delete":      func() error { _, err := s.Delete(ctx, "a:b", "c", "state"); return err }(),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
	}
	if k, err := s.key("ws", "f/a:b"); err != nil || k != "windmill-lite:res:ws:f/a:b" {
		t.Errorf("key = %q, %v", k, err)
	}
}

// TestContract runs the contract suite on one server.
func TestContract(t *testing.T) {
	c := direct(t)
	url := os.Getenv(urlEnv)
	storetest.Run(t, func(t *testing.T) wl.ResourceStore {
		s, _ := open(t, Options{URL: url, Prefix: testPrefix(t, c)})
		return s
	})
}

// TestContractSentinel runs the contract suite through Sentinel.
func TestContractSentinel(t *testing.T) {
	url := envOrSkip(t, sentinelEnv)
	c := direct(t)
	storetest.Run(t, func(t *testing.T) wl.ResourceStore {
		s, _ := open(t, Options{URL: url, Prefix: testPrefix(t, c)})
		return s
	})
}

// TestEntryLayout: an entry is one hash at <prefix>res:<workspace>:<path>
// with the fields t, v and w and no expiry; an update replaces v and w and
// keeps t; a delete removes the key.
func TestEntryLayout(t *testing.T) {
	c := direct(t)
	prefix := testPrefix(t, c)
	s, _ := open(t, Options{URL: os.Getenv(urlEnv), Prefix: prefix})
	ctx := context.Background()
	key := prefix + "res:ws-1:f/l/a:b"
	if ok, err := s.Create(ctx, "ws-1", "f/l/a:b", wl.Resource{Type: "state", Value: json.RawMessage(`{"n":1}`)}); err != nil || !ok {
		t.Fatalf("Create = %v, %v", ok, err)
	}
	fields := func() map[string]string {
		t.Helper()
		m, err := c.HGetAll(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := fields()
	if len(first) != 3 || first["t"] != "state" || first["v"] != `{"n":1}` || len(first["w"]) != 32 {
		t.Fatalf("the hash after Create: %v", first)
	}
	if ttl, err := c.TTL(ctx, key).Result(); err != nil || ttl != -1 {
		t.Errorf("TTL = %v, %v, want -1 (no expiry)", ttl, err)
	}
	if ok, err := s.UpdateValue(ctx, "ws-1", "f/l/a:b", "state", json.RawMessage(`2`)); err != nil || !ok {
		t.Fatalf("UpdateValue = %v, %v", ok, err)
	}
	second := fields()
	if len(second) != 3 || second["t"] != "state" || second["v"] != "2" || second["w"] == first["w"] || len(second["w"]) != 32 {
		t.Errorf("the hash after UpdateValue: %v, before %v", second, first)
	}
	if ok, err := s.Delete(ctx, "ws-1", "f/l/a:b", "state"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	if n, err := c.Exists(ctx, key).Result(); err != nil || n != 0 {
		t.Errorf("EXISTS after Delete = %d, %v", n, err)
	}
	// A key that is not a hash of the store's fields is an error, never a
	// missing entry.
	c.Set(ctx, prefix+"res:ws-1:f/l/string", "x", 0)
	c.HSet(ctx, prefix+"res:ws-1:f/l/partial", "t", "state")
	for _, p := range []string{"f/l/string", "f/l/partial"} {
		if _, ok, err := s.Get(ctx, "ws-1", p); err == nil || ok {
			t.Errorf("Get(%s) = %v, %v, want an error", p, ok, err)
		}
	}
}

// TestStoresShareThePrefix: two stores on one server with one prefix, as
// two processes have, see each other's entries; a store with another
// prefix sees none of them.
func TestStoresShareThePrefix(t *testing.T) {
	c := direct(t)
	prefix := testPrefix(t, c)
	url := os.Getenv(urlEnv)
	a, _ := open(t, Options{URL: url, Prefix: prefix})
	b, _ := open(t, Options{URL: url, Prefix: prefix})
	other, _ := open(t, Options{URL: url, Prefix: testPrefix(t, c)})
	ctx := context.Background()
	r := wl.Resource{Type: "state", Value: json.RawMessage(`"shared"`)}
	if ok, err := a.Create(ctx, "ws", "f/s/p", r); err != nil || !ok {
		t.Fatalf("Create = %v, %v", ok, err)
	}
	if got, ok, err := b.Get(ctx, "ws", "f/s/p"); err != nil || !ok || got.Type != r.Type || string(got.Value) != string(r.Value) {
		t.Errorf("the second store reads %+v %v %v", got, ok, err)
	}
	if ok, err := b.Create(ctx, "ws", "f/s/p", r); err != nil || ok {
		t.Errorf("the second store's Create = %v, %v, want false", ok, err)
	}
	if _, ok, err := other.Get(ctx, "ws", "f/s/p"); err != nil || ok {
		t.Errorf("a store with another prefix reads %v %v", ok, err)
	}
}

// TestEvictionCheck sets the server's maxmemory-policy to each kind and
// back: allkeys-* fails New unless AllowEviction, which warns instead;
// noeviction and volatile-* pass without a warning.
func TestEvictionCheck(t *testing.T) {
	c := direct(t)
	ctx := context.Background()
	was, err := c.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.ConfigSet(ctx, "maxmemory-policy", was["maxmemory-policy"]) })
	url := os.Getenv(urlEnv)
	for _, policy := range []string{"allkeys-lru", "allkeys-lfu", "allkeys-random", "noeviction", "volatile-lru", "volatile-ttl"} {
		if err := c.ConfigSet(ctx, "maxmemory-policy", policy).Err(); err != nil {
			t.Fatal(err)
		}
		evicts := strings.HasPrefix(policy, "allkeys-")
		var warnings []string
		s, err := New(ctx, Options{URL: url, Warn: func(m string) { warnings = append(warnings, m) }})
		if s != nil {
			s.Close()
		}
		switch {
		case evicts && (err == nil || !strings.Contains(err.Error(), "is "+policy+", which evicts keys without an expiry")):
			t.Errorf("%s: New = %v, want the refusal", policy, err)
		case !evicts && (err != nil || len(warnings) != 0):
			t.Errorf("%s: New = %v, warnings %q, want neither", policy, err, warnings)
		}
		warnings = nil
		s, err = New(ctx, Options{URL: url, AllowEviction: true, Warn: func(m string) { warnings = append(warnings, m) }})
		if err != nil {
			t.Fatalf("%s with AllowEviction: %v", policy, err)
		}
		s.Close()
		allowed := len(warnings) == 1 && strings.Contains(warnings[0], "eviction is allowed explicitly")
		if evicts && !allowed || !evicts && len(warnings) != 0 {
			t.Errorf("%s with AllowEviction: warnings %q", policy, warnings)
		}
	}
}

// TestUnreadablePolicy: a user that may not run CONFIG gets a warning,
// which holds no password, and a working store.
func TestUnreadablePolicy(t *testing.T) {
	c := direct(t)
	ctx := context.Background()
	const user, secret = "wl-test-noconfig", "s3cr3t-pw"
	if err := c.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+secret, "~*", "&*", "+@all", "-config").Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Do(ctx, "ACL", "DELUSER", user) })
	addr := c.Options().Addr
	s, warnings := open(t, Options{URL: fmt.Sprintf("redis://%s:%s@%s/%d", user, secret, addr, c.Options().DB), Prefix: testPrefix(t, c)})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cannot read the maxmemory-policy of Redis at "+addr) || !strings.Contains(warnings[0], "NOPERM") || strings.Contains(warnings[0], secret) {
		t.Errorf("warnings %q, want one about the unreadable policy", warnings)
	}
	if ok, err := s.Create(ctx, "ws", "f/u/p", wl.Resource{Type: "state", Value: json.RawMessage(`1`)}); err != nil || !ok {
		t.Errorf("Create = %v, %v", ok, err)
	}
	// A wrong password fails New without the password in the error.
	_, err := New(ctx, Options{URL: fmt.Sprintf("redis://%s:wrong-%s@%s", user, secret, addr)})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("a wrong password: %v", err)
	}
	if !slices.ContainsFunc([]string{"WRONGPASS", "invalid"}, func(s string) bool { return err != nil && strings.Contains(err.Error(), s) }) {
		t.Errorf("a wrong password: %v, want the server's refusal", err)
	}
}
