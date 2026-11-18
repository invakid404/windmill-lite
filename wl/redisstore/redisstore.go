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
	"net/url"
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
	// read_timeout, pool_size or max_retries. The URL carries the
	// password: New never puts it in an error or a warning.
	URL string
	// Prefix starts every key, so deployments can share one Redis; empty
	// means DefaultPrefix.
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
	client, target, err := connect(o.URL)
	if err != nil {
		return nil, err
	}
	s := &Store{client: client, prefix: o.Prefix, timeout: o.OperationTimeout, target: target}
	if s.prefix == "" {
		s.prefix = DefaultPrefix
	}
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
// returns a client for it, and the server's description for messages.
// Neither the error nor the description holds the URL's password.
func connect(raw string) (redis.UniversalClient, string, error) {
	if raw == "" {
		return nil, "", errors.New("redisstore: no Redis URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's error quotes the whole URL: only its reason is
		// kept, and anything that may be the user information is masked.
		var ue *url.Error
		reason := "it is not a URL"
		if errors.As(err, &ue) {
			reason = ue.Err.Error()
		}
		return nil, "", fmt.Errorf("redisstore: the Redis URL does not parse: %s", mask(reason, userinfoOf(raw)...))
	}
	secrets := secretsOf(u, raw)
	if u.Query().Has("master_name") {
		fo, err := redis.ParseFailoverURL(raw)
		if err != nil {
			return nil, "", fmt.Errorf("redisstore: the Redis URL does not parse: %s", mask(err.Error(), secrets...))
		}
		// Each operation's own deadline (Store.op) bounds go-redis's
		// reads, writes and retries.
		fo.ContextTimeoutEnabled = true
		target := fmt.Sprintf("the master %q through the Sentinels %s (database %d)", fo.MasterName, strings.Join(fo.SentinelAddrs, ", "), fo.DB)
		return redis.NewFailoverClient(fo), target, nil
	}
	o, err := redis.ParseURL(raw)
	if err != nil {
		return nil, "", fmt.Errorf("redisstore: the Redis URL does not parse: %s", mask(err.Error(), secrets...))
	}
	o.ContextTimeoutEnabled = true
	return redis.NewClient(o), fmt.Sprintf("%s (database %d)", o.Addr, o.DB), nil
}

// userinfoOf returns what may be the user information of a URL, which
// may not parse: the text between "://" and the last "@" before the path,
// and the part of it after its first colon, the password.
func userinfoOf(raw string) []string {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return nil
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	i := strings.LastIndex(rest, "@")
	if i < 0 {
		return nil
	}
	info := rest[:i]
	if _, password, ok := strings.Cut(info, ":"); ok {
		return []string{info, password}
	}
	return []string{info}
}

// secretsOf returns the passwords a parsed URL carries, as go-redis reads
// them: the user information's, and the query parameters password and
// sentinel_password, both as written and decoded.
func secretsOf(u *url.URL, raw string) []string {
	var s []string
	if p, ok := u.User.Password(); ok {
		s = append(s, p)
	}
	q := u.Query()
	for _, k := range []string{"password", "sentinel_password"} {
		s = append(s, q[k]...)
	}
	return append(s, userinfoOf(raw)...)
}

// mask replaces every secret in text with "xxxxx".
func mask(text string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, "xxxxx")
		}
	}
	return text
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
