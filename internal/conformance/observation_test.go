package conformance

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestObservationJSONKeysAreCanonicalSitePaths(t *testing.T) {
	no := false
	two := 2
	path := func(segs ...Segment) SitePath { return SitePath{}.Child(segs...) }
	obs := Observation{
		Status: RunFailure,
		Result: json.RawMessage(`{"error":{"message":"boom","name":"Error"}}`),
		Steps: map[SitePath]StepObservation{
			path(Module("b")):                  {Status: StepNotRun},
			path(Module("a/b")):                {Status: StepFailure, Args: json.RawMessage(`{"x":1}`)},
			path(FailureSlot()):                {Status: StepSuccess, Result: json.RawMessage(`1`)},
			path(Module("l"), Iteration(0)):    {Status: StepSuccess, Ran: &no},
			path(Module("br")):                 {Status: StepSuccess, BranchChosen: &BranchChosen{Type: "branch", Branch: &two}},
			path(RepeatedModule("a", 1)):       {Status: StepSkipped, Skipped: &no},
			path(Module("x"), Attempt(0)):      {Status: StepCanceled},
			path(Module("p"), DefaultBranch()): {Status: StepSuccess},
		},
		Effects: []json.RawMessage{json.RawMessage(`{"step":"a"}`)},
	}
	data, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"status":"failure","result":{"error":{"message":"boom","name":"Error"}},"steps":{` +
		`"m:a%2Fb":{"status":"failure","args":{"x":1}},` +
		`"m:a~1":{"status":"skipped","skipped":false},` +
		`"m:b":{"status":"not_run","ran":false},` +
		`"m:br":{"status":"success","branch_chosen":{"type":"branch","branch":2}},` +
		`"m:l/i:0":{"status":"success","ran":false},` +
		`"m:p/b:default":{"status":"success"},` +
		`"m:x/r:0":{"status":"canceled"},` +
		`"s:failure":{"status":"success","result":1}` +
		`},"effects":[{"step":"a"}]}`
	if string(data) != want {
		t.Fatalf("encoded:\n%s\nwant:\n%s", data, want)
	}
	var back Observation
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(back); string(again) != want {
		t.Errorf("round trip:\n%s", again)
	}
}

func TestObservationDecodingRejectsBadKeysAndStatuses(t *testing.T) {
	for name, doc := range map[string]string{
		"drafted slot path":  `{"status":"success","result":null,"steps":{"failure":{"status":"success"}}}`,
		"lower-case escape":  `{"status":"success","result":null,"steps":{"m:a%2fb":{"status":"success"}}}`,
		"unknown run status": `{"status":"done","result":null}`,
		"unknown step":       `{"status":"success","result":null,"steps":{"m:a":{"status":"waiting"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var obs Observation
			if err := json.Unmarshal([]byte(doc), &obs); err == nil {
				t.Errorf("decoded %s", doc)
			}
		})
	}
}

func TestStatusVocabulary(t *testing.T) {
	for _, s := range []RunStatus{RunSuccess, RunFailure, RunCanceled, RunSkipped, RunLatched, RunHalted} {
		if !s.Valid() {
			t.Errorf("%q not valid", s)
		}
		if got, want := s.HostDecided(), s == RunLatched || s == RunHalted; got != want {
			t.Errorf("%q HostDecided = %v", s, got)
		}
	}
	if RunStatus("not_run").Valid() || StepStatus("latched").Valid() {
		t.Error("run and step vocabularies overlap where they must not")
	}
	if _, err := json.Marshal(RunStatus("bogus")); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("marshalling an unknown status: %v", err)
	}
	if _, err := json.Marshal(StepStatus("")); err == nil {
		t.Error("marshalled an empty step status")
	}
}

func TestNotRunStepsCarryRanFalse(t *testing.T) {
	// spec: OR-V2/a — steps.b is {"status":"not_run","ran":false}.
	data, err := json.Marshal(StepObservation{Status: StepNotRun})
	if err != nil || string(data) != `{"status":"not_run","ran":false}` {
		t.Fatalf("encoded %s, %v", data, err)
	}
	var withRan, withoutRan StepObservation
	if err := json.Unmarshal([]byte(`{"status":"not_run","ran":false}`), &withRan); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"status":"not_run"}`), &withoutRan); err != nil {
		t.Fatal(err)
	}
	if withoutRan.Ran == nil || *withoutRan.Ran || withRan.Ran == nil || *withRan.Ran {
		t.Fatalf("decoded ran = %v and %v, want false", withRan.Ran, withoutRan.Ran)
	}
	yes := true
	if _, err := json.Marshal(StepObservation{Status: StepNotRun, Ran: &yes}); err == nil {
		t.Error("encoded a not_run step that ran")
	}
	if err := json.Unmarshal([]byte(`{"status":"not_run","ran":true}`), &withRan); err == nil {
		t.Error("decoded a not_run step that ran")
	}
	// A step that ran keeps ran absent, and an empty loop keeps ran false
	// (OR-V2/c).
	data, _ = json.Marshal(StepObservation{Status: StepSuccess})
	if string(data) != `{"status":"success"}` {
		t.Errorf("a step that ran encoded as %s", data)
	}
	no := false
	data, _ = json.Marshal(StepObservation{Status: StepSuccess, Ran: &no, Result: json.RawMessage(`[]`)})
	if string(data) != `{"status":"success","ran":false,"result":[]}` {
		t.Errorf("an empty loop encoded as %s", data)
	}
}
