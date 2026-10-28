package jsonval

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestProducerTextIsKeptVerbatim(t *testing.T) {
	// spec: JE-8, FX-42 — the producer's key order, spacing and number
	// spelling survive, because memory routes read exactly this text.
	for _, text := range []string{
		`{"b":1,"a":2}`,
		`{"a":1,"a":2}`,
		`-0`,
		`1e+21`,
		`{ "x" : [1, 2.50] }`,
		`"😀"`,
		`null`,
	} {
		v := FromProducer([]byte(text))
		if got := string(v.Producer()); got != text {
			t.Errorf("Producer() = %q, want %q", got, text)
		}
		if v.IsZero() {
			t.Errorf("%s: IsZero", text)
		}
		if v.String() != text {
			t.Errorf("String() = %q", v.String())
		}
	}
}

func TestHasNUL(t *testing.T) {
	// spec: OR-27, FX-48 — \u0000 in a string or a key has no stored form.
	tests := []struct {
		text string
		nul  bool
	}{
		{`"\u0000"`, true},
		{`{"\u0000":1}`, true},
		{`["a","b\u0000c"]`, true},
		{`{"k":"x","k2":{"deep":["\u0000"]}}`, true},
		{`{"a":"\u0000","a":1}`, true}, // rejected even though the duplicate is dropped later
		{`"\\u0000"`, false},           // an escaped backslash, then the letters u0000
		{`"\\\u0000"`, true},           // an escaped backslash, then a NUL escape
		{`"\\\\u0000"`, false},
		{`"\u00000"`, true},
		{`"\u0001"`, false},
		{`"\u000"`, false},
		{`"u0000"`, false},
		{`"\"\u0000"`, true},
		{`"\n\t"`, false},
		{`{"a":1}`, false},
		{`"\`, false},
		{``, false},
	}
	for _, tt := range tests {
		if got := FromProducer([]byte(tt.text)).HasNUL(); got != tt.nul {
			t.Errorf("HasNUL(%s) = %v, want %v", tt.text, got, tt.nul)
		}
	}
}

func TestStored(t *testing.T) {
	if _, err := FromProducer([]byte(`{"a":"\u0000"}`)).Stored(); !errors.Is(err, ErrNUL) {
		t.Errorf("Stored() of a NUL value = %v, want ErrNUL", err)
	}
	// Until A1 lands, every other value reports that it has no canonical
	// form yet, rather than a wrong one.
	if _, err := FromProducer([]byte(`{"b":1,"a":2}`)).Stored(); !errors.Is(err, ErrNotCanonicalised) {
		t.Errorf("Stored() = %v, want ErrNotCanonicalised", err)
	}
	if _, err := (Value{}).Stored(); err == nil {
		t.Error("Stored() of the zero Value succeeded")
	}
}

func TestStoredIsComputedOnceUnderConcurrency(t *testing.T) {
	v := FromProducer([]byte(`"\u0000"`))
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = v.Stored()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if !errors.Is(err, ErrNUL) {
			t.Fatalf("concurrent Stored() = %v", err)
		}
	}
}

func TestZeroValueIsNotNull(t *testing.T) {
	var zero Value
	if !zero.IsZero() || zero.HasNUL() || zero.Producer() != nil {
		t.Fatal("zero Value is not empty")
	}
	if FromProducer([]byte(`null`)).IsZero() {
		t.Fatal("JSON null is the zero Value")
	}
	if _, err := json.Marshal(zero); err == nil {
		t.Error("marshalled the zero Value")
	}
	type holder struct {
		V Value `json:"v,omitzero"`
	}
	data, err := json.Marshal(holder{})
	if err != nil || string(data) != `{}` {
		t.Errorf("omitzero: %s, %v", data, err)
	}
}

func TestJSONFieldRoundTripsCompactProducerText(t *testing.T) {
	type frame struct {
		Body Value `json:"body"`
	}
	var f frame
	in := `{"body":{"b":1,"a":2.50}}`
	if err := json.Unmarshal([]byte(in), &f); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Body.Producer()); got != `{"b":1,"a":2.50}` {
		t.Fatalf("decoded body = %s", got)
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Errorf("re-encoded = %s, want %s", out, in)
	}
}

func TestEncodingJSONRespellsTheText(t *testing.T) {
	// MarshalJSON returns the producer bytes, but encoding/json compacts
	// and HTML-escapes them; only the parsed value survives. Writers that
	// must keep the spelling use Producer() (see MarshalJSON).
	v := FromProducer([]byte(`{ "x" : "<job:m:a>" }`))
	raw, err := v.MarshalJSON()
	if err != nil || string(raw) != `{ "x" : "<job:m:a>" }` {
		t.Fatalf("MarshalJSON = %s, %v", raw, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"x":"\u003cjob:m:a\u003e"}`; string(out) != want {
		t.Errorf("json.Marshal = %s, want %s", out, want)
	}
	var a, b any
	if json.Unmarshal(out, &a) != nil || json.Unmarshal(v.Producer(), &b) != nil || a.(map[string]any)["x"] != b.(map[string]any)["x"] {
		t.Errorf("the parsed value changed: %v != %v", a, b)
	}
}

func TestUnmarshalJSONCopiesItsInput(t *testing.T) {
	buf := []byte(`"abc"`)
	var v Value
	if err := v.UnmarshalJSON(buf); err != nil {
		t.Fatal(err)
	}
	buf[1] = 'X'
	if got := string(v.Producer()); got != `"abc"` {
		t.Errorf("Value aliased the decoder's buffer: %s", got)
	}
}
