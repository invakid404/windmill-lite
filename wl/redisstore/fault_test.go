package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
	"github.com/redis/go-redis/v9"

	"github.com/invakid404/windmill-lite/wl"
)

// toxiproxyEnv names the address (host:port) of a Toxiproxy API whose
// proxies listen on that host and reach the server of WL_TEST_REDIS_URL.
const toxiproxyEnv = "WL_TEST_TOXIPROXY"

// noScriptReply is the reply to an EVALSHA after SCRIPT FLUSH, which
// go-redis answers with EVAL.
const noScriptReply = "-NOSCRIPT No matching script. Please use EVAL.\r\n"

// A cut is where a fault test breaks the store's connection.
type cut struct {
	name string
	// before drops every byte to Redis and closes the connection, so the
	// command never reaches Redis.
	before bool
	// after holds the reply back for 4 s, past go-redis's 2 s read
	// timeout, which closes the connection: the command ran, and its reply
	// is lost. After SCRIPT FLUSH it is EVALSHA's NOSCRIPT reply that is
	// lost, so EVAL never runs.
	after bool
	// mid lets half of the reply through, then closes the connection.
	mid bool
	// once lets go-redis's retry, on a new connection, through.
	once bool
}

var cuts = []cut{
	{name: "before", before: true},
	{name: "after", after: true},
	{name: "after-once", after: true, once: true},
	{name: "mid-reply", mid: true},
	{name: "mid-reply-once", mid: true, once: true},
}

// redial runs its armed function once, at the next dial of a connection:
// it removes a cut before go-redis's retry connects again.
type redial struct {
	mu    sync.Mutex
	armed func()
	fired atomic.Bool
}

func (h *redial) arm(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = f
	h.fired.Store(false)
}

// disarm drops the armed function, keeping whether it fired.
func (h *redial) disarm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = nil
}

func (h *redial) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		h.mu.Lock()
		f := h.armed
		h.armed = nil
		h.mu.Unlock()
		if f != nil {
			f()
			h.fired.Store(true)
		}
		return next(ctx, network, addr)
	}
}

func (h *redial) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }

func (h *redial) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// newProxy returns a Toxiproxy proxy to the server of c, deleted when t
// ends, and the address it listens on.
func newProxy(t *testing.T, c *redis.Client) (*toxiproxy.Proxy, string) {
	t.Helper()
	api := envOrSkip(t, toxiproxyEnv)
	host, _, err := net.SplitHostPort(api)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := toxiproxy.NewClient(api).CreateProxy("wl-test-"+writeID()[:8], net.JoinHostPort(host, "0"), c.Options().Addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxy.Delete() })
	_, port, err := net.SplitHostPort(proxy.Listen)
	if err != nil {
		t.Fatal(err)
	}
	return proxy, net.JoinHostPort(host, port)
}

// TestOperationTimeout: an operation whose reply Redis holds back ends
// after OperationTimeout, though go-redis's own read timeout and retries
// would wait longer.
func TestOperationTimeout(t *testing.T) {
	c := direct(t)
	proxy, addr := newProxy(t, c)
	s, _ := open(t, Options{URL: fmt.Sprintf("redis://%s/%d?read_timeout=5s&max_retries=3", addr, c.Options().DB), Prefix: testPrefix(t, c), OperationTimeout: time.Second})
	if _, err := proxy.AddToxic("slow", "latency", "downstream", 1, toxiproxy.Attributes{"latency": 4000}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err := s.Get(context.Background(), "ws", "f/t/slow")
	if d := time.Since(start); err == nil || d > 2*time.Second {
		t.Errorf("Get through a 4 s latency with a 1 s operation timeout: %v after %v", err, d)
	}
}

// TestFaults cuts the store's connection, through Toxiproxy, before each
// operation's command reaches Redis, after Redis ran it, and in the middle
// of its reply, once (go-redis's retry gets through) or on every attempt;
// with the server's script cache as it is and emptied by SCRIPT FLUSH, so
// that the cut also falls on go-redis's EVAL after the NOSCRIPT reply.
// Throughout, readers on a direct connection check that the entry is
// missing or one whole value a writer wrote. After each operation:
//
//   - an operation that errored left the entry whole: the old value or
//     the new one (or missing, for a create or a delete);
//   - a cut before the command changed nothing;
//   - a retried create reports true, and a competing create after it
//     false: true exactly once;
//   - a retried update and a retried get return as without the cut, and a
//     retried delete leaves the entry gone.
func TestFaults(t *testing.T) {
	c := direct(t)
	proxy, addr := newProxy(t, c)
	prefix := testPrefix(t, c)
	// One connection, so the cut falls on the connection the operation
	// uses; short timeouts and two retries keep the attempts quick.
	through, _ := open(t, Options{URL: fmt.Sprintf("redis://%s/%d?pool_size=1&read_timeout=2s&max_retries=2", addr, c.Options().DB), Prefix: prefix})
	hook := &redial{}
	through.client.AddHook(hook)
	plain, _ := open(t, Options{URL: envOrSkip(t, urlEnv), Prefix: prefix})

	ctx := context.Background()
	oldValue := json.RawMessage(`"old ` + strings.Repeat("o", 1<<20) + `"`)
	newValue := json.RawMessage(`"new ` + strings.Repeat("n", 1<<20) + `"`)
	for _, op := range []string{"get", "create", "update", "delete"} {
		for _, k := range cuts {
			for _, flush := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/flush=%v", op, k.name, flush)
				t.Run(name, func(t *testing.T) {
					path := "f/fault/" + strings.ReplaceAll(name, "/", "-")
					key := prefix + "res:ws:" + path
					if op != "create" {
						if ok, err := plain.Create(ctx, "ws", path, wl.Resource{Type: "state", Value: oldValue}); err != nil || !ok {
							t.Fatalf("seeding: %v %v", ok, err)
						}
					}
					// legal says whether an entry may be in a state while
					// the operation runs, and after it: a seeded get or
					// update always finds the entry, old (or new, for the
					// update); a create finds none or the new value; a
					// delete the old value or none.
					legal := func(present bool, v string) bool {
						switch op {
						case "get":
							return present && v == string(oldValue)
						case "update":
							return present && (v == string(oldValue) || v == string(newValue))
						case "create":
							return !present || v == string(newValue)
						default:
							return !present || v == string(oldValue)
						}
					}
					// Readers on a direct connection check every state in
					// between. Cleanup stops them, and removes the cut,
					// whichever way the case ends.
					stop := make(chan struct{})
					var stopOnce sync.Once
					var torn atomic.Value
					var reads atomic.Int64
					var wg sync.WaitGroup
					t.Cleanup(func() {
						stopOnce.Do(func() { close(stop) })
						wg.Wait()
						hook.disarm()
						proxy.RemoveToxic("cut")
					})
					for range 2 {
						wg.Go(func() {
							for {
								select {
								case <-stop:
									return
								default:
								}
								f, err := c.HMGet(ctx, key, "t", "v").Result()
								if err != nil {
									continue
								}
								reads.Add(1)
								present := f[0] != nil || f[1] != nil
								v, _ := f[1].(string)
								if present && f[0] != "state" || !legal(present, v) {
									torn.Store(fmt.Sprintf("a read of present %v, %.40v %.40v", present, f[0], f[1]))
								}
							}
						})
					}

					// The script cache is emptied, or holds the three
					// scripts, whatever an earlier case did to it.
					if flush {
						if err := c.ScriptFlush(ctx).Err(); err != nil {
							t.Fatal(err)
						}
					} else {
						for _, sc := range []*redis.Script{createScript, updateScript, deleteScript} {
							if err := sc.Load(ctx, c).Err(); err != nil {
								t.Fatal(err)
							}
						}
					}
					// The store's one connection is open before the cut,
					// so the operation's first attempt uses it.
					if err := through.client.Ping(ctx).Err(); err != nil {
						t.Fatal(err)
					}
					switch {
					case k.before:
						if _, err := proxy.AddToxic("cut", "timeout", "upstream", 1, toxiproxy.Attributes{"timeout": 100}); err != nil {
							t.Fatal(err)
						}
					case k.after:
						if _, err := proxy.AddToxic("cut", "latency", "downstream", 1, toxiproxy.Attributes{"latency": 4000}); err != nil {
							t.Fatal(err)
						}
					case k.mid:
						// The bytes that get through, after which the
						// connection closes: after a flush the NOSCRIPT
						// reply, so that the cut falls on EVAL's reply;
						// then half of the operation's own reply, ":1\r\n"
						// or the value.
						bytes, half := 0, 2
						if flush && op != "get" {
							bytes = len(noScriptReply)
						}
						if op == "get" {
							half = len(oldValue) / 2
						}
						if _, err := proxy.AddToxic("cut", "limit_data", "downstream", 1, toxiproxy.Attributes{"bytes": bytes + half}); err != nil {
							t.Fatal(err)
						}
					}
					if k.once {
						hook.arm(func() { proxy.RemoveToxic("cut") })
					}

					var (
						got   wl.Resource
						found bool
						ok    bool
						opErr error
					)
					switch op {
					case "get":
						got, found, opErr = through.Get(ctx, "ws", path)
					case "create":
						ok, opErr = through.Create(ctx, "ws", path, wl.Resource{Type: "state", Value: newValue})
					case "update":
						ok, opErr = through.UpdateValue(ctx, "ws", path, "state", newValue)
					case "delete":
						ok, opErr = through.Delete(ctx, "ws", path, "state")
					}

					hook.disarm()
					proxy.RemoveToxic("cut")
					time.Sleep(50 * time.Millisecond)
					stopOnce.Do(func() { close(stop) })
					wg.Wait()
					if v := torn.Load(); v != nil {
						t.Fatalf("a reader saw a state the operation cannot leave: %s", v)
					}
					if reads.Load() == 0 {
						t.Fatal("no read finished")
					}

					after, present, err := plain.Get(ctx, "ws", path)
					if err != nil {
						t.Fatal(err)
					}
					if present && after.Type != "state" || !legal(present, string(after.Value)) {
						t.Fatalf("after the operation the entry is in a state it cannot leave: present %v, %.60s", present, after.Value)
					}
					if k.once && !hook.fired.Load() {
						t.Fatalf("go-redis did not connect again after the cut: %v", opErr)
					}
					if !k.once {
						if opErr == nil {
							t.Fatalf("the operation succeeded through the cut: found %v ok %v", found, ok)
						}
						switch {
						case k.before, k.after && flush:
							// Nothing reached Redis, or only EVALSHA, whose
							// NOSCRIPT reply was lost, so EVAL was never
							// sent: the entry is as before.
							wantPresent := op != "create"
							if present != wantPresent || present && string(after.Value) != string(oldValue) {
								t.Errorf("an operation that never ran changed the entry: present %v", present)
							}
						case op == "delete":
							// The command ran before its reply was cut.
							if present {
								t.Errorf("a delete cut in its reply left the entry")
							}
						case op != "get":
							if !present || string(after.Value) != string(newValue) {
								t.Errorf("a %s cut in its reply did not apply: present %v", op, present)
							}
						}
						t.Logf("error %v; entry present %v", opErr, present)
						return
					}
					if opErr != nil {
						t.Fatalf("the retried operation failed: %v", opErr)
					}
					switch op {
					case "get":
						if !found || string(got.Value) != string(oldValue) {
							t.Errorf("the retried get read found %v, %d bytes", found, len(got.Value))
						}
					case "create":
						if !ok || !present || string(after.Value) != string(newValue) {
							t.Errorf("the retried create reported %v; entry present %v", ok, present)
						}
						if again, err := plain.Create(ctx, "ws", path, wl.Resource{Type: "state", Value: oldValue}); err != nil || again {
							t.Errorf("a competing create after it reported %v, %v, want false", again, err)
						}
					case "update":
						if !ok || string(after.Value) != string(newValue) {
							t.Errorf("the retried update reported %v; entry present %v", ok, present)
						}
					case "delete":
						if present {
							t.Errorf("the retried delete (reported %v) left the entry", ok)
						}
						t.Logf("the retried delete reported %v", ok)
					}
				})
			}
		}
	}
}
