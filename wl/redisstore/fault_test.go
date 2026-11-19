package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
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
// proxies listen on that host. faultsEnv names the Redis server the fault
// tests proxy to, which no other test uses: they count the commands it
// runs, so another client's commands would read as the store's.
const (
	toxiproxyEnv = "WL_TEST_TOXIPROXY"
	faultsEnv    = "WL_TEST_REDIS_FAULTS_URL"
)

// faultsServer returns a go-redis client of the fault tests' own server,
// closed when t ends, and its URL.
func faultsServer(t *testing.T) (*redis.Client, string) {
	t.Helper()
	url := envOrSkip(t, faultsEnv)
	o, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("%s does not parse", faultsEnv)
	}
	c := redis.NewClient(o)
	t.Cleanup(func() { c.Close() })
	return c, url
}

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
	// lost, so EVAL is never sent.
	after bool
	// mid lets half of the reply through, then closes the connection.
	mid bool
	// reopen removes the cut when the client dials a new connection, so a
	// command sent again would reach Redis and show in its counts.
	reopen bool
}

var cuts = []cut{
	{name: "before", before: true},
	{name: "after", after: true},
	{name: "mid-reply", mid: true},
	{name: "after-reopen", after: true, reopen: true},
	{name: "mid-reply-reopen", mid: true, reopen: true},
}

// reopener runs its armed function at the next dial of a connection.
type reopener struct {
	mu    sync.Mutex
	armed func()
}

func (h *reopener) arm(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = f
}

func (h *reopener) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		h.mu.Lock()
		f := h.armed
		h.armed = nil
		h.mu.Unlock()
		if f != nil {
			f()
		}
		return next(ctx, network, addr)
	}
}

func (h *reopener) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }

func (h *reopener) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// attempts records, through go-redis's own process hook, the data commands
// the store's client runs (evalsha, eval, hmget): with no retries, each is
// one attempt to send it.
type attempts struct {
	mu    sync.Mutex
	names []string
}

func (h *attempts) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := h.names
	h.names = nil
	return names
}

func (h *attempts) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *attempts) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if dataCommand(cmd.Name()) {
			h.mu.Lock()
			h.names = append(h.names, cmd.Name())
			h.mu.Unlock()
		}
		return next(ctx, cmd)
	}
}

func (h *attempts) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// dataCommand reports whether name is one of the store's data commands.
func dataCommand(name string) bool {
	return name == "evalsha" || name == "eval" || name == "hmget"
}

// calls returns how many times Redis has run each data command (evalsha,
// eval, hmget), from INFO commandstats: what the server executed, NOSCRIPT
// replies included. Only the fault tests use their server (faultsEnv), and
// during an operation only the store under test runs these commands there;
// the readers use HGETALL.
func calls(t *testing.T, c *redis.Client) map[string]int {
	t.Helper()
	info, err := c.Info(context.Background(), "commandstats").Result()
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, line := range strings.Split(info, "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), ":calls=")
		name = strings.TrimPrefix(name, "cmdstat_")
		if !ok || !dataCommand(name) {
			continue
		}
		count, _, _ := strings.Cut(rest, ",")
		v, err := strconv.Atoi(count)
		if err != nil {
			t.Fatalf("INFO commandstats: %s", line)
		}
		n[name] = v
	}
	return n
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
// after OperationTimeout, though go-redis's own read timeout would wait
// longer.
func TestOperationTimeout(t *testing.T) {
	c, _ := faultsServer(t)
	proxy, addr := newProxy(t, c)
	s, _ := open(t, Options{URL: fmt.Sprintf("redis://%s/%d?read_timeout=5s", addr, c.Options().DB), Prefix: testPrefix(t, c), OperationTimeout: time.Second})
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
// of its reply; with the server's script cache as it is and emptied by
// SCRIPT FLUSH, so that the cut also falls on go-redis's EVAL after the
// NOSCRIPT reply. Throughout, readers on a direct connection check that
// the entry is in a state the operation can leave. After each operation:
//
//   - the operation returns an error; the store's client ran exactly the
//     permitted data commands (go-redis's process hook): one HMGET, or one
//     EVALSHA and, only after a NOSCRIPT reply that arrived, one EVAL; and
//     Redis ran each of them once, or none when cut before them (its INFO
//     commandstats counts): nothing was sent again (noRetries);
//   - a command that never ran, cut before it or after a NOSCRIPT reply
//     that was lost, changed nothing;
//   - a write cut in its reply was applied once: a create holds its value
//     and a competing create after it reports false, an update holds its
//     value, a delete left no entry;
//   - the store's next operation, once the cut is gone, succeeds.
func TestFaults(t *testing.T) {
	c, url := faultsServer(t)
	proxy, addr := newProxy(t, c)
	prefix := testPrefix(t, c)
	// One connection, so the cut falls on the connection the operation
	// uses; a short read timeout keeps the cut after the command quick.
	// Attempts are what the store's client runs, through its process hook;
	// executions are what Redis ran, from its own command counts.
	hook := &attempts{}
	client, target, err := connect(fmt.Sprintf("redis://%s/%d?pool_size=1&read_timeout=2s", addr, c.Options().DB), DefaultOperationTimeout)
	if err != nil {
		t.Fatal("the proxy's URL does not connect")
	}
	client.AddHook(hook)
	reopen := &reopener{}
	client.AddHook(reopen)
	t.Cleanup(func() { client.Close() })
	through := &Store{client: client, prefix: prefix, timeout: DefaultOperationTimeout, target: target}
	if err := through.check(context.Background(), false, func(string) {}); err != nil {
		t.Fatal(err)
	}
	plain, _ := open(t, Options{URL: url, Prefix: prefix})

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
								// HGETALL, so that every HMGET Redis runs is the
								// store's.
								f, err := c.HGetAll(ctx, key).Result()
								if err != nil {
									continue
								}
								reads.Add(1)
								present := len(f) > 0
								if present && f["t"] != "state" || !legal(present, f["v"]) {
									torn.Store(fmt.Sprintf("a read of present %v, %.40v %.40v", present, f["t"], f["v"]))
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
					// so the operation uses it.
					if err := through.client.Ping(ctx).Err(); err != nil {
						t.Fatal(err)
					}
					switch {
					case k.before:
						// The connection closes 2 s after the cut, well after
						// the operation has written its command into it.
						if _, err := proxy.AddToxic("cut", "timeout", "upstream", 1, toxiproxy.Attributes{"timeout": 2000}); err != nil {
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

					if k.reopen {
						reopen.arm(func() { proxy.RemoveToxic("cut") })
					}
					hook.take()
					before := calls(t, c)
					var (
						found, ok bool
						opErr     error
					)
					switch op {
					case "get":
						_, found, opErr = through.Get(ctx, "ws", path)
					case "create":
						ok, opErr = through.Create(ctx, "ws", path, wl.Resource{Type: "state", Value: newValue})
					case "update":
						ok, opErr = through.UpdateValue(ctx, "ws", path, "state", newValue)
					case "delete":
						ok, opErr = through.Delete(ctx, "ws", path, "state")
					}
					tried := hook.take()

					reopen.arm(nil)
					proxy.RemoveToxic("cut")
					time.Sleep(50 * time.Millisecond)
					now := calls(t, c)
					var executed []string
					for _, name := range []string{"evalsha", "eval", "hmget"} {
						for range now[name] - before[name] {
							executed = append(executed, name)
						}
					}
					stopOnce.Do(func() { close(stop) })
					wg.Wait()
					if v := torn.Load(); v != nil {
						t.Fatalf("a reader saw a state the operation cannot leave: %s", v)
					}
					if reads.Load() == 0 {
						t.Fatal("no read finished")
					}
					if opErr == nil {
						t.Fatalf("the operation succeeded through the cut: found %v ok %v", found, ok)
					}
					// The permitted sequences: a get runs one HMGET, a write one
					// EVALSHA, and one EVAL only after a NOSCRIPT reply that
					// arrived (a cut in the middle of the reply after a flush).
					// Redis runs nothing cut before it, and each command that
					// reached it once: a second run is a replay.
					data := "evalsha"
					if op == "get" {
						data = "hmget"
					}
					wantTried := []string{data}
					if k.mid && flush && op != "get" {
						wantTried = []string{"evalsha", "eval"}
					}
					wantRan := wantTried
					if k.before {
						wantRan = nil
					}
					if !slices.Equal(tried, wantTried) {
						t.Fatalf("the store's client ran %v, want %v (%v)", tried, wantTried, opErr)
					}
					if !slices.Equal(executed, wantRan) {
						t.Fatalf("Redis ran %v, want %v (%v)", executed, wantRan, opErr)
					}

					after, present, err := plain.Get(ctx, "ws", path)
					if err != nil {
						t.Fatal(err)
					}
					if present && after.Type != "state" || !legal(present, string(after.Value)) {
						t.Fatalf("after the operation the entry is in a state it cannot leave: present %v, %.60s", present, after.Value)
					}
					switch {
					case k.before, k.after && flush:
						// Nothing reached Redis, or only EVALSHA, whose
						// NOSCRIPT reply was lost, so EVAL was never sent:
						// the entry is as before.
						wantPresent := op != "create"
						if present != wantPresent || present && string(after.Value) != string(oldValue) {
							t.Errorf("an operation that never ran changed the entry: present %v", present)
						}
					case op == "create":
						// Applied once, though the call reported an error.
						if !present || string(after.Value) != string(newValue) {
							t.Errorf("a create cut in its reply did not apply: present %v", present)
						}
						if again, err := plain.Create(ctx, "ws", path, wl.Resource{Type: "state", Value: oldValue}); err != nil || again {
							t.Errorf("a competing create after it reported %v, %v, want false", again, err)
						}
					case op == "update":
						if !present || string(after.Value) != string(newValue) {
							t.Errorf("an update cut in its reply did not apply: present %v", present)
						}
					case op == "delete":
						if present {
							t.Errorf("a delete cut in its reply left the entry")
						}
					}
					// The cut is gone: the store's next operation, on a new
					// connection, succeeds.
					if _, _, err := through.Get(ctx, "ws", path); err != nil {
						t.Errorf("the store's next operation failed: %v", err)
					}
					t.Logf("error %v; entry present %v", opErr, present)
				})
			}
		}
	}
}
