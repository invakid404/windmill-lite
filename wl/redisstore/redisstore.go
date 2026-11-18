// Package redisstore is a wl.ResourceStore on Redis, so that runtimes in
// several processes share the resources their steps write, such as the
// state of the stock client's setState, and keep them as long as Redis
// keeps its data.
//
// It is built on go-redis (github.com/redis/go-redis/v9): its client, its
// URL parsers, its connection pool, its retries and its script helper.
// Each entry is one Redis hash at <prefix>res:<workspace>:<path>, with the
// fields t (the type), v (the value's bytes) and w (the id of the write
// that left them). Each operation is one command on that one key, or one
// Lua script on it, so each is atomic: Redis runs a script with no other
// command in between.
package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/invakid404/windmill-lite/wl"
)

// DefaultPrefix is the prefix of every key when Options.Prefix is empty.
const DefaultPrefix = "windmill-lite:"

// DefaultOperationTimeout bounds each operation when
// Options.OperationTimeout is zero: go-redis's retries included, an
// operation returns an error after 10 s. It stays well under the 30 s
// after which a host-mode engine's watchdog gives up on a silent host.
const DefaultOperationTimeout = 10 * time.Second

// Options configures New.
type Options struct {
	// URL names the Redis server, and is parsed by go-redis:
	//
	//   - redis://[user:password@]host:port/db, or rediss:// for TLS: one
	//     server, parsed by go-redis's ParseURL.
	//   - The same form with the query parameter master_name: host:port
	//     and each addr query parameter name a Sentinel, which gives the
	//     current master of that name, parsed by go-redis's
	//     ParseFailoverURL. The user and password of the URL are the
	//     Sentinels'; the query parameters username and password are the
	//     master's.
	//
	// Other query parameters set go-redis's options, such as dial_timeout,
	// read_timeout, pool_size or max_retries. The Sentinel options that
	// route to replicas (route_by_latency, route_randomly, replica_only,
	// use_disconnected_replicas) are refused: the store reads and writes the
	// master. The URL carries the password: no error or warning of New
	// holds any part of a URL that does not parse.
	URL string
	// Prefix starts every key, so deployments can share one Redis: one or
	// more characters without a colon, then a colon, such as "app-1:", so
	// that two prefixes never name one key; empty means DefaultPrefix.
	Prefix string
	// AllowEviction lets New succeed on a server whose maxmemory-policy is
	// allkeys-*, which evicts keys that have no expiry, such as every
	// entry of this store. New warns instead.
	AllowEviction bool
	// OperationTimeout bounds each operation, retries included, and New's
	// own checks; zero means DefaultOperationTimeout.
	OperationTimeout time.Duration
	// Warn receives each warning of New, such as a maxmemory-policy that
	// cannot be read; nil writes them with the standard log package.
	Warn func(message string)
}

// Store is a wl.ResourceStore on Redis. It is safe for concurrent use.
// Close releases its connections.
type Store struct {
	client  redis.UniversalClient
	prefix  string
	timeout time.Duration
	// Target names the server for messages: its address and database, or
	// the master's name and the Sentinels. It never holds a password.
	target string
}

var _ wl.ResourceStore = (*Store)(nil)

// New connects to the Redis that o.URL names, checks it, and returns the
// store. It fails when the URL is not one that go-redis parses, when the
// server cannot be reached or refuses the scripts, and when its
// maxmemory-policy evicts keys without an expiry (allkeys-*) and
// o.AllowEviction is false. A policy that cannot be read, because CONFIG
// is renamed or the user may not run it, is a warning.
func New(ctx context.Context, o Options) (*Store, error) {
	prefix := o.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if !validPrefix(prefix) {
		return nil, fmt.Errorf("redisstore: the key prefix %q must be one or more characters without a colon, then a colon, such as %q", prefix, DefaultPrefix)
	}
	client, target, err := connect(o.URL)
	if err != nil {
		return nil, err
	}
	s := &Store{client: client, prefix: prefix, timeout: o.OperationTimeout, target: target}
	if s.timeout <= 0 {
		s.timeout = DefaultOperationTimeout
	}
	warn := o.Warn
	if warn == nil {
		warn = func(m string) { log.Print(m) }
	}
	if err := s.check(ctx, o.AllowEviction, warn); err != nil {
		client.Close()
		return nil, err
	}
	return s, nil
}

// connect parses url with the go-redis parser that its form needs and
// returns a client for it, and the server's description for messages: its
// address and database, or the Sentinels' addresses and the database, as
// go-redis parsed them. A URL that does not parse gets an error that names
// no part of it, since any part may be a secret pasted in the wrong place.
func connect(raw string) (redis.UniversalClient, string, error) {
	if raw == "" {
		return nil, "", errors.New("redisstore: no Redis URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", errors.New(badURL)
	}
	if u.Query().Has("master_name") {
		fo, err := redis.ParseFailoverURL(raw)
		if err != nil {
			return nil, "", errors.New(badURL)
		}
		// go-redis takes each addr parameter as written: one that is not
		// a plain host and port, such as a URL with a password pasted
		// there, would reach the messages through the dial's error.
		for _, a := range fo.SentinelAddrs {
			if !plainAddr(a) {
				return nil, "", errors.New(badURL)
			}
		}
		// The store reads and writes the master only: go-redis routes
		// these to replicas, or refuses them in NewFailoverClient.
		if fo.RouteByLatency || fo.RouteRandomly || fo.ReplicaOnly || fo.UseDisconnectedReplicas {
			return nil, "", errors.New("redisstore: the Sentinel URL asks for replicas (route_by_latency, route_randomly, replica_only or use_disconnected_replicas), and the store reads and writes the master only")
		}
		// Each operation's own deadline (Store.op) bounds go-redis's
		// reads, writes and retries.
		fo.ContextTimeoutEnabled = true
		target := fmt.Sprintf("the master of the Sentinels %s (database %d)", strings.Join(fo.SentinelAddrs, ", "), fo.DB)
		return redis.NewFailoverClient(fo), target, nil
	}
	o, err := redis.ParseURL(raw)
	if err != nil {
		return nil, "", errors.New(badURL)
	}
	o.ContextTimeoutEnabled = true
	return redis.NewClient(o), fmt.Sprintf("%s (database %d)", o.Addr, o.DB), nil
}

// badURL is the error of a URL that no go-redis parser accepts.
const badURL = "redisstore: the Redis URL does not parse: it must be redis:// or rediss://, with a host, an optional port, an optional database number and query parameters that go-redis knows, or a Sentinel URL with master_name"

// plainAddr reports whether addr is a host, with no user information or
// path, and a numeric port.
func plainAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(host, "@/?#") {
		return false
	}
	_, err = strconv.ParseUint(port, 10, 16)
	return err == nil
}

// validPrefix reports whether prefix is one or more characters with no
// colon, then one colon. Each key is then <prefix>res:<workspace>:<path>
// with its first colon at the prefix's end, and a workspace id holds no
// colon, so two different (prefix, workspace, path) never name one key.
func validPrefix(prefix string) bool {
	head, ok := strings.CutSuffix(prefix, ":")
	return ok && head != "" && !strings.Contains(head, ":")
}

// check pings the server, loads the scripts, and checks its
// maxmemory-policy, within one operation's timeout.
func (s *Store) check(ctx context.Context, allowEviction bool, warn func(string)) error {
	ctx, cancel := s.op(ctx)
	defer cancel()
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redisstore: cannot reach Redis at %s: %w", s.target, err)
	}
	for _, sc := range []*redis.Script{createScript, updateScript, deleteScript} {
		if err := sc.Load(ctx, s.client).Err(); err != nil {
			return fmt.Errorf("redisstore: Redis at %s refused the store's scripts: %w", s.target, err)
		}
	}
	policies, err := s.client.ConfigGet(ctx, "maxmemory-policy").Result()
	policy, read := policies["maxmemory-policy"]
	if err != nil {
		var redisErr redis.Error
		if !errors.As(err, &redisErr) {
			return fmt.Errorf("redisstore: reading the maxmemory-policy of Redis at %s: %w", s.target, err)
		}
		warn(fmt.Sprintf("redisstore: cannot read the maxmemory-policy of Redis at %s (%v); a policy that evicts keys without an expiry (allkeys-*) would drop the store's entries", s.target, err))
		return nil
	}
	if !read {
		warn(fmt.Sprintf("redisstore: Redis at %s did not report its maxmemory-policy; a policy that evicts keys without an expiry (allkeys-*) would drop the store's entries", s.target))
		return nil
	}
	switch Evicts(policy) {
	case EvictsEntries:
		if !allowEviction {
			return fmt.Errorf("redisstore: the maxmemory-policy of Redis at %s is %s, which evicts keys without an expiry, such as the store's entries: set it to noeviction or a volatile-* policy, or allow eviction explicitly", s.target, policy)
		}
		warn(fmt.Sprintf("redisstore: the maxmemory-policy of Redis at %s is %s, which can evict the store's entries; eviction is allowed explicitly", s.target, policy))
	case KeepsEntries:
	default:
		warn(fmt.Sprintf("redisstore: the maxmemory-policy of Redis at %s is %q, which redisstore does not know; a policy that evicts keys without an expiry would drop the store's entries", s.target, policy))
	}
	return nil
}

// Eviction says what a maxmemory-policy does to the store's entries, which
// have no expiry.
type Eviction int

const (
	// UnknownPolicy is a policy name that Evicts does not know.
	UnknownPolicy Eviction = iota
	// KeepsEntries: noeviction, which refuses writes when memory is full,
	// and the volatile-* policies, which evict only keys with an expiry.
	KeepsEntries
	// EvictsEntries: the allkeys-* policies, which evict any key.
	EvictsEntries
)

// Evicts classifies a maxmemory-policy.
func Evicts(policy string) Eviction {
	switch {
	case policy == "noeviction", strings.HasPrefix(policy, "volatile-"):
		return KeepsEntries
	case strings.HasPrefix(policy, "allkeys-"):
		return EvictsEntries
	default:
		return UnknownPolicy
	}
}

// Close releases the store's connections. Operations after Close fail.
func (s *Store) Close() error {
	return s.client.Close()
}

// The scripts each touch the one key KEYS[1], the entry's hash.
var (
	// createScript: ARGV is the type, the value and the write id. An
	// existing key is a taken path, unless its write id is this write's,
	// which means an earlier attempt of the same Create was applied and
	// its reply lost (go-redis retried it): 1 then, as that attempt
	// reported. Otherwise the hash is written whole: 1.
	createScript = redis.NewScript(`if redis.call('EXISTS', KEYS[1]) == 1 then
  if redis.call('HGET', KEYS[1], 'w') == ARGV[3] then return 1 end
  return 0
end
redis.call('HSET', KEYS[1], 't', ARGV[1], 'v', ARGV[2], 'w', ARGV[3])
return 1`)
	// updateScript: ARGV is the expected type, the value and the write id.
	// No key, or a key of another type, is 0, creating and changing
	// nothing. Otherwise the value and the write id are replaced, the type
	// kept: 1. A retried attempt writes the same value again.
	updateScript = redis.NewScript(`if redis.call('HGET', KEYS[1], 't') ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], 'v', ARGV[2], 'w', ARGV[3])
return 1`)
	// deleteScript: ARGV is the expected type. No key, or a key of another
	// type, is 0, changing nothing. Otherwise the key is deleted: 1.
	deleteScript = redis.NewScript(`if redis.call('HGET', KEYS[1], 't') ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
return 1`)
)

// key returns the key of the entry at path in workspace. A workspace id
// holds no colon, so the key names one (workspace, path) only; one that
// does is refused.
func (s *Store) key(workspace, path string) (string, error) {
	if strings.Contains(workspace, ":") {
		return "", fmt.Errorf("redisstore: the workspace id %q contains a colon, which the store's keys cannot tell from the path", workspace)
	}
	return s.prefix + "res:" + workspace + ":" + path, nil
}

// op returns ctx bounded by the store's operation timeout.
func (s *Store) op(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// writeID returns a new random write id.
func writeID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Get returns the entry at path in workspace: one HMGET of its type and
// value.
func (s *Store) Get(ctx context.Context, workspace, path string) (wl.Resource, bool, error) {
	key, err := s.key(workspace, path)
	if err != nil {
		return wl.Resource{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return wl.Resource{}, false, err
	}
	ctx, cancel := s.op(ctx)
	defer cancel()
	fields, err := s.client.HMGet(ctx, key, "t", "v").Result()
	if err != nil {
		return wl.Resource{}, false, fmt.Errorf("redisstore: get: %w", err)
	}
	if fields[0] == nil && fields[1] == nil {
		return wl.Resource{}, false, nil
	}
	t, tok := fields[0].(string)
	v, vok := fields[1].(string)
	if !tok || !vok {
		return wl.Resource{}, false, fmt.Errorf("redisstore: get: the key %q is not an entry of the store: it lacks the field t or v", key)
	}
	return wl.Resource{Type: t, Value: json.RawMessage(v)}, true, nil
}

// Create adds r at path in workspace, unless the path holds an entry: one
// script.
func (s *Store) Create(ctx context.Context, workspace, path string, r wl.Resource) (bool, error) {
	key, err := s.key(workspace, path)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ctx, cancel := s.op(ctx)
	defer cancel()
	n, err := createScript.Run(ctx, s.client, []string{key}, r.Type, []byte(r.Value), writeID()).Int()
	if err != nil {
		return false, fmt.Errorf("redisstore: create: %w", err)
	}
	return n == 1, nil
}

// UpdateValue replaces the value of the entry of type typ at path in
// workspace: one script.
func (s *Store) UpdateValue(ctx context.Context, workspace, path, typ string, value json.RawMessage) (bool, error) {
	key, err := s.key(workspace, path)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ctx, cancel := s.op(ctx)
	defer cancel()
	n, err := updateScript.Run(ctx, s.client, []string{key}, typ, []byte(value), writeID()).Int()
	if err != nil {
		return false, fmt.Errorf("redisstore: update: %w", err)
	}
	return n == 1, nil
}

// Delete removes the entry of type typ at path in workspace: one script.
// go-redis retries a command whose connection broke, so a Delete that was
// applied and whose reply was lost reports false: the retry finds no
// entry. The entry is gone either way.
func (s *Store) Delete(ctx context.Context, workspace, path, typ string) (bool, error) {
	key, err := s.key(workspace, path)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ctx, cancel := s.op(ctx)
	defer cancel()
	n, err := deleteScript.Run(ctx, s.client, []string{key}, typ).Int()
	if err != nil {
		return false, fmt.Errorf("redisstore: delete: %w", err)
	}
	return n == 1, nil
}
