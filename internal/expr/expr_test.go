package expr

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPredicateSites(t *testing.T) {
	// spec: JE-24 — the boolean sites, and only they, are wrapped as
	// Boolean(<text>) and strictly checked.
	want := map[SiteKind]bool{
		SiteInputTransform:        false,
		SiteFailureInputTransform: false,
		SiteIterator:              false,
		SiteParallelism:           false,
		SiteBranchOne:             true,
		SiteSkipIf:                true,
		SiteStopAfterIf:           true,
		SiteStopAfterAllItersIf:   true,
		SiteSleep:                 false,
		SiteTimeout:               false,
		SiteRetryIf:               true,
		SiteSkipExpr:              true,
	}
	if len(want) != len(siteNames)-1 {
		t.Fatalf("test covers %d sites, package defines %d", len(want), len(siteNames)-1)
	}
	for k, pred := range want {
		if k.Predicate() != pred {
			t.Errorf("%v.Predicate() = %v, want %v", k, k.Predicate(), pred)
		}
	}
	if SiteKind(0).Predicate() || SiteKind(0).Valid() {
		t.Error("the zero SiteKind is a site")
	}
}

func TestSiteKindText(t *testing.T) {
	type ir struct {
		Site SiteKind `json:"site"`
	}
	for k := SiteInputTransform; k.Valid(); k++ {
		data, err := json.Marshal(ir{k})
		if err != nil {
			t.Fatal(err)
		}
		var back ir
		if err := json.Unmarshal(data, &back); err != nil || back.Site != k {
			t.Errorf("%v: round trip %s gave %v, %v", k, data, back.Site, err)
		}
		if strings.HasPrefix(k.String(), "SiteKind(") {
			t.Errorf("%d has no name", uint8(k))
		}
	}
	if _, err := json.Marshal(ir{SiteKind(99)}); err == nil {
		t.Error("marshalled an unknown site")
	}
	var back ir
	if err := json.Unmarshal([]byte(`{"site":"suspend"}`), &back); err == nil {
		t.Error("accepted the suspend site, which load rejects")
	}
}

func TestDefaultLimitsAreWindmills(t *testing.T) {
	// spec: JE-2 (20 s per attempt), JE-3 (128 MiB), JE-48 (QuickJS-ng's
	// default stack of 1 MiB).
	l := DefaultLimits()
	if l.MemoryMB != 128 || l.Attempt != 20*time.Second || l.StackBytes != 1<<20 {
		t.Fatalf("DefaultLimits() = %+v", l)
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Limits{
		{MemoryMB: 0, Attempt: time.Second, StackBytes: 1},
		{MemoryMB: 1, Attempt: 0, StackBytes: 1},
		{MemoryMB: 1, Attempt: time.Second, StackBytes: -1},
	} {
		if bad.Validate() == nil {
			t.Errorf("Validate(%+v) accepted it", bad)
		}
	}
}

// TestExprJSON checks the compiled expression's JSON form, which the IR
// carries (ADR 0002): field names, the mode's name, and the omitted parts
// of an expression the pipeline has not compiled yet.
func TestExprJSON(t *testing.T) {
	bare := Expr{Site: SiteSkipIf, Text: "x"}
	data, err := json.Marshal(bare)
	if err != nil || string(data) != `{"site":"skip_if","text":"x"}` {
		t.Fatalf("Marshal(bare) = %s, %v", data, err)
	}
	id := "a"
	full := Expr{
		Site: SiteInputTransform, Text: " results.a ", Trimmed: "results.a",
		Shortcut: Shortcut{PreviousID: &id, ResultsPath: &ResultsPath{Step: "a"}},
		Mode:     ModeExpression, Binds: []string{"results"}, Source: "s",
	}
	data, err = json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var back Expr
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(back)
	if string(again) != string(data) || !strings.Contains(string(data), `"mode":"expression"`) {
		t.Errorf("round trip: %s then %s", data, again)
	}
	if _, err := json.Marshal(Expr{Site: SiteSkipIf, Mode: Mode(9)}); err == nil {
		t.Error("marshalled an unknown mode")
	}
	if err := json.Unmarshal([]byte(`{"site":"skip_if","text":"","mode":"other"}`), &back); err == nil {
		t.Error("accepted an unknown mode")
	}
}
