// Package storetest is the contract suite of wl.ResourceStore: the tests
// every resource store must pass, the memory store and a host's own alike.
// A store's test calls Run with a function that makes a new, empty store:
//
//	func TestMyStore(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) wl.ResourceStore { return newMyStore(t) })
//	}
//
// Run checks the contract that wl.ResourceStore's doc comment states:
// missing entries, byte-exact values, create at a taken path, update and
// delete, workspace isolation, unusual paths and large values, returned
// bytes that are the caller's, canceled contexts, and atomicity under
// concurrent writers and readers. Run it with -race. The atomicity tests
// last 5 s each; with -short they last 1 s.
package storetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/wl"
)

// Run runs the contract suite, each test on a new store that newStore
// makes. Every test is a subtest of t.
func Run(t *testing.T, newStore func(t *testing.T) wl.ResourceStore) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.test(t, newStore(t)) })
	}
}

// Names returns the names of the suite's tests, in the order Run runs them.
func Names() []string {
	names := make([]string, len(cases))
	for i, c := range cases {
		names[i] = c.name
	}
	return names
}

var cases = []struct {
	name string
	test func(t *testing.T, s wl.ResourceStore)
}{
	{"MissingGet", missingGet},
	{"RoundTripIsByteExact", roundTrip},
	{"CreateAtTakenPath", createTaken},
	{"UpdateKeepsTheType", updateKeepsType},
	{"OtherTypesAreKept", otherTypes},
	{"UpdateMissingCreatesNothing", updateMissing},
	{"Delete", deleteEntry},
	{"WorkspacesAreApart", workspaces},
	{"Paths", paths},
	{"Values", values},
	{"ReturnedBytesAreTheCallers", notAliased},
	{"CanceledContext", canceled},
	{"NoTornRead", noTornRead},
	{"CreateRace", createRace},
	{"InterleavedWritesEndLegal", interleaved},
}

const ws = "storetest"

func mustGet(t *testing.T, s wl.ResourceStore, workspace, path string) (wl.Resource, bool) {
	t.Helper()
	r, ok, err := s.Get(context.Background(), workspace, path)
	if err != nil {
		t.Fatalf("Get(%q, %q): %v", workspace, path, err)
	}
	return r, ok
}

func mustCreate(t *testing.T, s wl.ResourceStore, workspace, path string, r wl.Resource) bool {
	t.Helper()
	ok, err := s.Create(context.Background(), workspace, path, r)
	if err != nil {
		t.Fatalf("Create(%q, %q): %v", workspace, path, err)
	}
	return ok
}

func wantEntry(t *testing.T, s wl.ResourceStore, workspace, path string, want wl.Resource) {
	t.Helper()
	got, ok := mustGet(t, s, workspace, path)
	if !ok {
		t.Fatalf("Get(%q, %q): no entry, want type %q value %.80s", workspace, path, want.Type, want.Value)
	}
	if got.Type != want.Type || !bytes.Equal(got.Value, want.Value) {
		t.Fatalf("Get(%q, %q) = type %q value %.80s, want type %q value %.80s", workspace, path, got.Type, got.Value, want.Type, want.Value)
	}
}

func wantNone(t *testing.T, s wl.ResourceStore, workspace, path string) {
	t.Helper()
	if got, ok := mustGet(t, s, workspace, path); ok {
		t.Fatalf("Get(%q, %q) = type %q value %.80s, want no entry", workspace, path, got.Type, got.Value)
	}
}

// missingGet: a path with no entry reads as false, with no error.
func missingGet(t *testing.T, s wl.ResourceStore) {
	wantNone(t, s, ws, "f/st/missing")
}

// roundTrip: a value comes back byte for byte, its spacing, key order and
// number spelling included.
func roundTrip(t *testing.T, s wl.ResourceStore) {
	r := wl.Resource{Type: "state", Value: json.RawMessage(" {\"b\": 1.50, \"a\":[1e3, -0, \"\\u0041\"]}\n")}
	if !mustCreate(t, s, ws, "f/st/exact", r) {
		t.Fatal("Create of a new path reported false")
	}
	wantEntry(t, s, ws, "f/st/exact", r)
}

// createTaken: a create at a taken path reports false and changes nothing.
func createTaken(t *testing.T, s wl.ResourceStore) {
	first := wl.Resource{Type: "state", Value: json.RawMessage(`{"n":1}`)}
	mustCreate(t, s, ws, "f/st/taken", first)
	if mustCreate(t, s, ws, "f/st/taken", wl.Resource{Type: "other", Value: json.RawMessage(`{"n":2}`)}) {
		t.Fatal("Create at a taken path reported true")
	}
	wantEntry(t, s, ws, "f/st/taken", first)
}

// updateKeepsType: an update replaces the value and keeps the type.
func updateKeepsType(t *testing.T, s wl.ResourceStore) {
	mustCreate(t, s, ws, "f/st/upd", wl.Resource{Type: "state", Value: json.RawMessage(`1`)})
	ok, err := s.UpdateValue(context.Background(), ws, "f/st/upd", json.RawMessage(`{"n":2}`))
	if err != nil || !ok {
		t.Fatalf("UpdateValue = %v, %v; want true, nil", ok, err)
	}
	wantEntry(t, s, ws, "f/st/upd", wl.Resource{Type: "state", Value: json.RawMessage(`{"n":2}`)})
}

// otherTypes: an entry of a type other than state keeps that type through
// a round trip and an update; a store that keeps no type fails.
func otherTypes(t *testing.T, s wl.ResourceStore) {
	for _, p := range typeProblems(s) {
		t.Error(p)
	}
}

// typeProblems runs otherTypes' checks and returns what it found wrong.
func typeProblems(s wl.ResourceStore) (problems []string) {
	ctx := context.Background()
	r := wl.Resource{Type: "wl_other", Value: json.RawMessage(`{"k":"v"}`)}
	if ok, err := s.Create(ctx, ws, "f/st/other", r); err != nil || !ok {
		return []string{fmt.Sprintf("Create = %v, %v; want true, nil", ok, err)}
	}
	if got, ok, err := s.Get(ctx, ws, "f/st/other"); err != nil || !ok || got.Type != "wl_other" {
		problems = append(problems, fmt.Sprintf("after Create, Get = type %q, %v, %v; want type wl_other", got.Type, ok, err))
	}
	if ok, err := s.UpdateValue(ctx, ws, "f/st/other", json.RawMessage(`2`)); err != nil || !ok {
		return append(problems, fmt.Sprintf("UpdateValue = %v, %v; want true, nil", ok, err))
	}
	if got, ok, err := s.Get(ctx, ws, "f/st/other"); err != nil || !ok || got.Type != "wl_other" || string(got.Value) != "2" {
		problems = append(problems, fmt.Sprintf("after UpdateValue, Get = type %q value %s, %v, %v; want type wl_other value 2", got.Type, got.Value, ok, err))
	}
	return problems
}

// updateMissing: an update of a missing path reports false and creates
// nothing.
func updateMissing(t *testing.T, s wl.ResourceStore) {
	ok, err := s.UpdateValue(context.Background(), ws, "f/st/none", json.RawMessage(`1`))
	if err != nil || ok {
		t.Fatalf("UpdateValue of a missing path = %v, %v; want false, nil", ok, err)
	}
	wantNone(t, s, ws, "f/st/none")
}

// deleteEntry: a delete removes the entry and reports true; a second
// reports false; the path can then be created again.
func deleteEntry(t *testing.T, s wl.ResourceStore) {
	mustCreate(t, s, ws, "f/st/del", wl.Resource{Type: "state", Value: json.RawMessage(`1`)})
	for i, want := range []bool{true, false} {
		ok, err := s.Delete(context.Background(), ws, "f/st/del")
		if err != nil || ok != want {
			t.Fatalf("Delete #%d = %v, %v; want %v, nil", i+1, ok, err, want)
		}
		wantNone(t, s, ws, "f/st/del")
	}
	if !mustCreate(t, s, ws, "f/st/del", wl.Resource{Type: "state", Value: json.RawMessage(`2`)}) {
		t.Fatal("Create after a delete reported false")
	}
}

// workspaces: the same path in two workspaces names two entries, which
// every operation keeps apart.
func workspaces(t *testing.T, s wl.ResourceStore) {
	a := wl.Resource{Type: "state", Value: json.RawMessage(`"a"`)}
	b := wl.Resource{Type: "state", Value: json.RawMessage(`"b"`)}
	mustCreate(t, s, "ws-a", "f/st/p", a)
	wantNone(t, s, "ws-b", "f/st/p")
	if !mustCreate(t, s, "ws-b", "f/st/p", b) {
		t.Fatal("Create in a second workspace reported false")
	}
	if ok, err := s.UpdateValue(context.Background(), "ws-b", "f/st/p", json.RawMessage(`"b2"`)); err != nil || !ok {
		t.Fatalf("UpdateValue = %v, %v", ok, err)
	}
	wantEntry(t, s, "ws-a", "f/st/p", a)
	if ok, err := s.Delete(context.Background(), "ws-a", "f/st/p"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	wantEntry(t, s, "ws-b", "f/st/p", wl.Resource{Type: "state", Value: json.RawMessage(`"b2"`)})
}

// paths: paths with slashes, a colon, a percent sign, non-ASCII letters
// and 1 KiB are keys like any other, and do not collide.
func paths(t *testing.T, s wl.ResourceStore) {
	ps := []string{"f/st/a/b/c/d", "f/st/a:b", "f/st/a%2Fb", "f/st/a/b", "f/st/ü€😀", "u/x/" + strings.Repeat("p", 1024)}
	for i, p := range ps {
		mustCreate(t, s, ws, p, wl.Resource{Type: "state", Value: json.RawMessage(fmt.Sprint(i))})
	}
	for i, p := range ps {
		wantEntry(t, s, ws, p, wl.Resource{Type: "state", Value: json.RawMessage(fmt.Sprint(i))})
	}
}

// values: null, an empty object and a 32 MiB value are kept whole.
func values(t *testing.T, s wl.ResourceStore) {
	big := make([]byte, 0, 32<<20+2)
	big = append(big, '"')
	big = append(big, bytes.Repeat([]byte("x"), 32<<20)...)
	big = append(big, '"')
	for i, v := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`{}`), big} {
		p := fmt.Sprintf("f/st/v%d", i)
		mustCreate(t, s, ws, p, wl.Resource{Type: "state", Value: v})
		wantEntry(t, s, ws, p, wl.Resource{Type: "state", Value: v})
	}
}

// notAliased: changing the bytes a caller passed in, or got back, changes
// no entry.
func notAliased(t *testing.T, s wl.ResourceStore) {
	in := []byte(`{"n":1}`)
	mustCreate(t, s, ws, "f/st/alias", wl.Resource{Type: "state", Value: in})
	in[5] = '9'
	got, _ := mustGet(t, s, ws, "f/st/alias")
	got.Value[5] = '8'
	wantEntry(t, s, ws, "f/st/alias", wl.Resource{Type: "state", Value: json.RawMessage(`{"n":1}`)})
	upd := []byte(`{"n":2}`)
	if ok, err := s.UpdateValue(context.Background(), ws, "f/st/alias", upd); err != nil || !ok {
		t.Fatalf("UpdateValue = %v, %v", ok, err)
	}
	upd[5] = '7'
	wantEntry(t, s, ws, "f/st/alias", wl.Resource{Type: "state", Value: json.RawMessage(`{"n":2}`)})
}

// canceled: with a canceled context every operation returns an error and
// changes nothing.
func canceled(t *testing.T, s wl.ResourceStore) {
	kept := wl.Resource{Type: "state", Value: json.RawMessage(`"kept"`)}
	mustCreate(t, s, ws, "f/st/kept", kept)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Get(ctx, ws, "f/st/kept"); err == nil {
		t.Error("Get with a canceled context returned no error")
	}
	if _, err := s.Create(ctx, ws, "f/st/new", wl.Resource{Type: "state", Value: json.RawMessage(`1`)}); err == nil {
		t.Error("Create with a canceled context returned no error")
	}
	if _, err := s.UpdateValue(ctx, ws, "f/st/kept", json.RawMessage(`"changed"`)); err == nil {
		t.Error("UpdateValue with a canceled context returned no error")
	}
	if _, err := s.Delete(ctx, ws, "f/st/kept"); err == nil {
		t.Error("Delete with a canceled context returned no error")
	}
	wantNone(t, s, ws, "f/st/new")
	wantEntry(t, s, ws, "f/st/kept", kept)
}

// duration is how long each atomicity test runs.
func duration() time.Duration {
	if testing.Short() {
		return time.Second
	}
	return 5 * time.Second
}

// selfValidating is a value that names its writer and proves itself whole:
// a JSON string of the writer's id repeated to size bytes, a bar, then the
// hex SHA-256 of that body.
func selfValidating(id string, size int) json.RawMessage {
	body := strings.Repeat(id, size/len(id)+1)[:size]
	sum := sha256.Sum256([]byte(body))
	return json.RawMessage(`"` + body + "|" + hex.EncodeToString(sum[:]) + `"`)
}

// writerOf returns the writer a whole self-validating value names, and
// false for a value that is not whole.
func writerOf(v json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", false
	}
	body, sum, ok := strings.Cut(s, "|")
	if !ok || len(body) == 0 {
		return "", false
	}
	got := sha256.Sum256([]byte(body))
	if hex.EncodeToString(got[:]) != sum {
		return "", false
	}
	id, _, _ := strings.Cut(body, ".")
	return id + ".", true
}

// noTornRead: 8 writers update one path with 1 MiB self-validating values
// while 8 readers read it; every update reports true, and every read is one
// whole value that a writer wrote, with the type the create gave, never a
// missing entry.
func noTornRead(t *testing.T, s wl.ResourceStore) {
	problems, reads, writes := tornReads(s, duration())
	for _, p := range problems {
		t.Error(p)
	}
	if reads == 0 || writes == 0 {
		t.Fatalf("%d reads and %d writes in %s", reads, writes, duration())
	}
	t.Logf("%d writes and %d reads of 1 MiB", writes, reads)
}

// tornReads runs noTornRead's writers and readers for d, and returns what
// the readers found wrong, at most one problem each, and the counts.
func tornReads(s wl.ResourceStore, d time.Duration) (problems []string, reads, writes int64) {
	const writers, readers, size = 8, 8, 1 << 20
	vals := make([]json.RawMessage, writers)
	known := map[string]bool{}
	for i := range vals {
		id := fmt.Sprintf("w%d.", i)
		vals[i], known[id] = selfValidating(id, size), true
	}
	if ok, err := s.Create(context.Background(), ws, "f/st/torn", wl.Resource{Type: "state", Value: vals[0]}); err != nil || !ok {
		return []string{fmt.Sprintf("the seeding Create = %v, %v; want true, nil", ok, err)}, 0, 0
	}
	deadline := time.Now().Add(d)
	var wg sync.WaitGroup
	var nr, nw atomic.Int64
	errs := make(chan string, writers+readers)
	for i := range writers {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				ok, err := s.UpdateValue(context.Background(), ws, "f/st/torn", vals[i])
				if err != nil || !ok {
					errs <- fmt.Sprintf("UpdateValue of the seeded path = %v, %v; want true, nil", ok, err)
					return
				}
				nw.Add(1)
			}
		})
	}
	for range readers {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				r, ok, err := s.Get(context.Background(), ws, "f/st/torn")
				nr.Add(1)
				switch {
				case err != nil:
					errs <- "Get: " + err.Error()
					return
				case !ok:
					// Nothing deletes the path in this phase: an update
					// that removes the entry for a moment is a torn write.
					errs <- "a read found no entry while updates replaced it"
					return
				case r.Type != "state":
					errs <- "a read has type " + r.Type
					return
				}
				if id, whole := writerOf(r.Value); !whole || !known[id] {
					errs <- fmt.Sprintf("a read is not one whole written value: %d bytes, %.40s…", len(r.Value), r.Value)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		problems = append(problems, e)
	}
	return problems, nr.Load(), nw.Load()
}

// createRace: of 64 concurrent creates at one path, exactly one reports
// true, and the entry holds its value.
func createRace(t *testing.T, s wl.ResourceStore) {
	winners, errs := raceCreates(s)
	for _, e := range errs {
		t.Error(e)
	}
	if len(winners) != 1 {
		t.Fatalf("Creates %v reported true, want exactly one", winners)
	}
	wantEntry(t, s, ws, "f/st/race", wl.Resource{Type: "state", Value: json.RawMessage(fmt.Sprint(winners[0]))})
}

// raceCreates starts 64 creates at one path together, and returns the
// numbers of those that reported true.
func raceCreates(s wl.ResourceStore) (winners []int, errs []string) {
	const n = 64
	var wg sync.WaitGroup
	won := make([]bool, n)
	failed := make([]string, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			ok, err := s.Create(context.Background(), ws, "f/st/race", wl.Resource{Type: "state", Value: json.RawMessage(fmt.Sprint(i))})
			if err != nil {
				failed[i] = fmt.Sprintf("Create #%d: %v", i, err)
			}
			won[i] = ok
		})
	}
	close(start)
	wg.Wait()
	for i := range n {
		if won[i] {
			winners = append(winners, i)
		}
		if failed[i] != "" {
			errs = append(errs, failed[i])
		}
	}
	return winners, errs
}

// interleaved: creates, updates, deletes and reads of one path in any
// order leave it, at every read and at the end, missing or holding one
// whole value a writer wrote, of the type the creates give.
func interleaved(t *testing.T, s wl.ResourceStore) {
	const workers, size = 8, 64 << 10
	known := map[string]bool{}
	vals := make([]json.RawMessage, workers)
	for i := range vals {
		id := fmt.Sprintf("i%d.", i)
		vals[i], known[id] = selfValidating(id, size), true
	}
	check := func(r wl.Resource, ok bool) string {
		if !ok {
			return ""
		}
		if r.Type != "state" {
			return "type " + r.Type
		}
		if id, whole := writerOf(r.Value); !whole || !known[id] {
			return fmt.Sprintf("a value that is not one whole written value: %.40s…", r.Value)
		}
		return ""
	}
	deadline := time.Now().Add(duration())
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := range workers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(i), 1))
			for time.Now().Before(deadline) {
				var err error
				switch rng.IntN(4) {
				case 0:
					_, err = s.Create(context.Background(), ws, "f/st/mix", wl.Resource{Type: "state", Value: vals[i]})
				case 1:
					_, err = s.UpdateValue(context.Background(), ws, "f/st/mix", vals[i])
				case 2:
					_, err = s.Delete(context.Background(), ws, "f/st/mix")
				default:
					var r wl.Resource
					var ok bool
					r, ok, err = s.Get(context.Background(), ws, "f/st/mix")
					if bad := check(r, ok); err == nil && bad != "" {
						errs <- "a read: " + bad
						return
					}
				}
				if err != nil {
					errs <- err.Error()
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if bad := check(mustGet(t, s, ws, "f/st/mix")); bad != "" {
		t.Errorf("at the end: %s", bad)
	}
}
