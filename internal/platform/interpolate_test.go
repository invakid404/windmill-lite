package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// interpolationCase is one case of testdata/interpolation.json.
type interpolationCase struct {
	Spec  string `json:"spec"`
	Title string `json:"title"`
	Setup struct {
		Variables []struct {
			Path      string `json:"path"`
			Value     string `json:"value"`
			IsSecret  bool   `json:"is_secret"`
			Encrypted bool   `json:"encrypted"`
		} `json:"variables"`
		Resources []struct {
			Path         string          `json:"path"`
			ResourceType string          `json:"resource_type"`
			Value        json.RawMessage `json:"value"`
			Nest         *int            `json:"nest"`
			Leaf         json.RawMessage `json:"leaf"`
			Repeat       *repeat         `json:"repeat"`
		} `json:"resources"`
	} `json:"setup"`
	Read *struct {
		Contract string            `json:"contract"`
		Path     string            `json:"path"`
		Job      map[string]string `json:"job"`
	} `json:"read"`
	Resolve *struct {
		Args     json.RawMessage   `json:"args"`
		ArgsNest *nestSpec         `json:"args_nest"`
		Env      map[string]string `json:"env"`
	} `json:"resolve"`
	Expect struct {
		Status int             `json:"status"`
		Body   *string         `json:"body"`
		Value  json.RawMessage `json:"value"`
		Prefix string          `json:"prefix"`
		Text   *string         `json:"text"`
		Error  string          `json:"error"`
		Nest   *nestSpec       `json:"nest"`
		Repeat *repeat         `json:"repeat"`
		Leaf   json.RawMessage `json:"leaf"`
	} `json:"expect"`
}

type repeat struct {
	Key  string `json:"key"`
	Item string `json:"item"`
	N    int    `json:"n"`
}

func (r *repeat) text() string {
	items := make([]string, r.N)
	for i := range items {
		items[i] = mustJSON(r.Item)
	}
	return `{` + mustJSON(r.Key) + `:[` + strings.Join(items, ",") + `]}`
}

// nestSpec is an argument object {key: nest(n)}, where nest(0) is leaf
// and nest(n) is {"k": nest(n-1)}, or, with inner, {key: {inner: nest(n),
// and beside's members}}.
type nestSpec struct {
	Key    string                     `json:"key"`
	Inner  string                     `json:"inner"`
	N      int                        `json:"n"`
	Leaf   json.RawMessage            `json:"leaf"`
	Beside map[string]json.RawMessage `json:"beside"`
}

func (n *nestSpec) text() string {
	v := nest(n.N, string(n.Leaf))
	if n.Inner != "" {
		inner := `{`
		for k, b := range n.Beside {
			inner += mustJSON(k) + `:` + string(b) + `,`
		}
		v = inner + mustJSON(n.Inner) + `:` + v + `}`
	}
	return `{` + mustJSON(n.Key) + `:` + v + `}`
}

func nest(n int, leaf string) string {
	return strings.Repeat(`{"k":`, n) + leaf + strings.Repeat(`}`, n)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// storeArgs is the engine's storage boundary for an argument object: each
// member's value in its stored form, the members in order (ADR 0005).
func storeArgs(t *testing.T, args []byte) jsonval.Value {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(args))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	out := []byte{'{'}
	for i := 0; dec.More(); i++ {
		k, _ := dec.Token()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		stored, err := jsonval.FromProducer(raw).Stored()
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			out = append(out, ',')
		}
		out = append(append(appendString(out, k.(string)), ':'), stored...)
	}
	return jsonval.FromProducer(append(out, '}'))
}

func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

// TestInterpolationCases runs testdata/interpolation.json: the walker's
// three contracts (WA-40) against the catalogue cases of WA-10 to WA-12,
// WA-V3, WA-42, JE-42, WR-5, FX-42/h, FX-42/i, FX-42/k, FX-42/l and FX-51/c.
func TestInterpolationCases(t *testing.T) {
	data, err := os.ReadFile("testdata/interpolation.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []interpolationCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("no cases")
	}
	ctx := context.Background()
	for _, c := range file.Cases {
		t.Run(c.Spec+" "+c.Title, func(t *testing.T) {
			if c.Spec == "" {
				t.Fatal("a case cites no spec id")
			}
			store := NewStore()
			for _, v := range c.Setup.Variables {
				store.SetVariable(v.Path, Variable{Value: v.Value, IsSecret: v.IsSecret, Encrypted: v.Encrypted})
			}
			for _, r := range c.Setup.Resources {
				value := r.Value
				switch {
				case r.Nest != nil:
					value = json.RawMessage(nest(*r.Nest, string(r.Leaf)))
				case r.Repeat != nil:
					value = json.RawMessage(r.Repeat.text())
				}
				if err := store.SetResource(r.Path, Resource{Type: r.ResourceType, Value: value}); err != nil {
					t.Fatal(err)
				}
			}
			s := &Services{Resources: store, Variables: store}
			switch {
			case c.Read != nil:
				status, body := read(ctx, s, c.Read.Contract, c.Read.Path, c.Read.Job)
				if status != c.Expect.Status {
					t.Fatalf("status %d (%s), want %d", status, body, c.Expect.Status)
				}
				checkText(t, body, c.Expect.Body, c.Expect.Value, c.Expect.Prefix, c.Expect.Nest, c.Expect.Repeat, c.Expect.Leaf)
			case c.Resolve != nil:
				args := c.Resolve.Args
				if c.Resolve.ArgsNest != nil {
					args = json.RawMessage(c.Resolve.ArgsNest.text())
				}
				env := c.Resolve.Env
				out, err := s.ResolveArgs(ctx, storeArgs(t, args), env, APIEnv(env))
				if c.Expect.Error != "" || (c.Expect.Prefix != "" && err != nil) {
					var pe *Error
					if !errors.As(err, &pe) {
						t.Fatalf("got %s, %v; want a failure", out, err)
					}
					if c.Expect.Error != "" && pe.Body() != c.Expect.Error {
						t.Fatalf("failure %q, want %q", pe.Body(), c.Expect.Error)
					}
					if !strings.HasPrefix(pe.Body(), c.Expect.Prefix) {
						t.Fatalf("failure %q, want the prefix %q", pe.Body(), c.Expect.Prefix)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var nested *nestSpec
				if c.Expect.Nest != nil {
					nested = c.Expect.Nest
				}
				if nested != nil {
					if want := nested.text(); !sameJSON(t, out.Producer(), []byte(want)) {
						t.Fatalf("got %s, want %s", out.Producer(), want)
					}
					return
				}
				checkText(t, string(out.Producer()), c.Expect.Text, c.Expect.Value, c.Expect.Prefix, nil, nil, nil)
			default:
				t.Fatal("a case neither reads nor resolves")
			}
		})
	}
}

// read reads through one contract, as the facade answers it: the status
// and the body.
func read(ctx context.Context, s *Services, contract, path string, job map[string]string) (int, string) {
	var (
		body []byte
		err  error
	)
	switch contract {
	case "raw":
		body, err = s.RawResource(ctx, path)
	case "interpolated":
		body, err = s.InterpolatedResource(ctx, path, job)
	case "variable":
		var v string
		if v, err = s.VariableValue(ctx, path); err == nil {
			body = appendString(nil, v)
		}
	case "resource_exists", "variable_exists":
		var ok bool
		if contract == "resource_exists" {
			ok, err = s.ResourceExists(ctx, path)
		} else {
			ok, err = s.VariableExists(ctx, path)
		}
		body, _ = json.Marshal(ok)
	default:
		panic("contract " + contract)
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.HTTPStatus(), pe.Body()
	}
	if err != nil {
		return 0, err.Error()
	}
	return 200, string(body)
}

func checkText(t *testing.T, got string, body *string, value json.RawMessage, prefix string, nested *nestSpec, rep *repeat, leaf json.RawMessage) {
	t.Helper()
	switch {
	case body != nil && got != *body:
		t.Fatalf("got %s, want %s", got, *body)
	case value != nil && !sameJSON(t, []byte(got), value):
		t.Fatalf("got %s, want the value %s", got, value)
	case prefix != "" && !strings.HasPrefix(got, prefix):
		t.Fatalf("got %s, want the prefix %s", got, prefix)
	case rep != nil && got != rep.text():
		t.Fatalf("got %.200s…, want %.200s…", got, rep.text())
	case nested != nil:
		l := nested.Leaf
		if len(l) == 0 {
			l = leaf
		}
		if want := nest(nested.N, string(l)); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	}
}

// The facade's expect reads a nest without a key: an interpolated read of
// nest(49) is the value itself.
func (n *nestSpec) UnmarshalJSON(b []byte) error {
	type plain nestSpec
	var p plain
	if err := json.Unmarshal(b, &p); err == nil {
		*n = nestSpec(p)
		return nil
	}
	var depth int
	if err := json.Unmarshal(b, &depth); err != nil {
		return err
	}
	n.N = depth
	return nil
}

// TestSecrets: a secret's plaintext is revealed by Secrets and passes
// through the value resolvers; ciphertext with no host resolver fails with
// secret_unavailable, in a read and in worker resolution (ADR 0007).
func TestSecrets(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	store.SetVariable("u/x/plain", Variable{Value: "p", IsSecret: true})
	store.SetVariable("u/x/cipher", Variable{Value: "AAAA", IsSecret: true, Encrypted: true})
	store.SetVariable("u/x/marked", Variable{Value: "$secret:k", IsSecret: true})
	s := &Services{Resources: store, Variables: store}
	if v, err := s.VariableValue(ctx, "u/x/plain"); err != nil || v != "p" {
		t.Fatalf("plain: %q %v", v, err)
	}
	_, err := s.VariableValue(ctx, "u/x/cipher")
	var pe *Error
	if !errors.As(err, &pe) || pe.HTTPStatus() != 500 || !strings.HasPrefix(pe.Body(), "Internal: secret_unavailable: the secret variable u/x/cipher is ciphertext") {
		t.Fatalf("cipher: %v", err)
	}
	if _, err := s.ResolveArgs(ctx, jsonval.FromProducer([]byte(`{"a":"$var:u/x/cipher"}`)), nil, nil); err == nil ||
		!strings.HasPrefix(err.Error(), "Not found: Variable u/x/cipher not found for `a`: Internal: secret_unavailable:") {
		t.Fatalf("cipher argument: %v", err)
	}
	if _, err := s.ResolveArgs(ctx, jsonval.FromProducer([]byte(`{"a":"$encrypted:AAAA"}`)), nil, nil); err == nil ||
		!strings.HasPrefix(err.Error(), "Internal: secret_unavailable: an $encrypted: value") {
		t.Fatalf("$encrypted: %v", err)
	}

	// A host's resolver chain replaces its own marker, in a secret and in
	// a resource value after interpolation, and a host's secret service
	// decrypts.
	store.SetResource("u/x/db", Resource{Value: json.RawMessage(`{"pw":"$secret:db","plain":"$var:u/x/plain"}`)})
	s.Resolvers = []ValueResolver{markerResolver{}}
	s.Secrets = hostSecrets{}
	if v, err := s.VariableValue(ctx, "u/x/marked"); err != nil || v != "resolved:k" {
		t.Fatalf("marked: %q %v", v, err)
	}
	if b, err := s.InterpolatedResource(ctx, "u/x/db", nil); err != nil || string(b) != `{"pw":"resolved:db","plain":"p"}` {
		t.Fatalf("db: %s %v", b, err)
	}
	if v, err := s.VariableValue(ctx, "u/x/cipher"); err != nil || v != "decrypted:AAAA" {
		t.Fatalf("host cipher: %q %v", v, err)
	}
	if out, err := s.ResolveArgs(ctx, jsonval.FromProducer([]byte(`{"a":"$encrypted:[1]"}`)), nil, nil); err != nil || string(out.Producer()) != `{"a":[1]}` {
		t.Fatalf("host $encrypted: %s %v", out.Producer(), err)
	}
}

type markerResolver struct{}

func (markerResolver) ResolveValue(_ context.Context, _ string, _ bool, v jsonval.Value) (jsonval.Value, error) {
	return jsonval.FromProducer(bytes.ReplaceAll(v.Producer(), []byte(`"$secret:`), []byte(`"resolved:`))), nil
}

type hostSecrets struct{}

func (hostSecrets) Reveal(_ context.Context, _ string, v Variable) (string, error) {
	if v.Encrypted {
		return "decrypted:" + v.Value, nil
	}
	return v.Value, nil
}

func (hostSecrets) Decrypt(_ context.Context, c string) (string, error) { return c, nil }

// TestChains: the first service of a chain that has a path answers for it.
func TestChains(t *testing.T) {
	ctx := context.Background()
	host, bundle := NewStore(), NewStore()
	host.SetVariable("u/x/v", Variable{Value: "host"})
	bundle.SetVariable("u/x/v", Variable{Value: "bundle"})
	bundle.SetVariable("u/x/w", Variable{Value: "w"})
	if err := bundle.SetResource("u/x/r", Resource{Value: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	s := &Services{Variables: VariableChain{host, bundle}, Resources: ResourceChain{host, bundle}}
	for path, want := range map[string]string{"u/x/v": "host", "u/x/w": "w"} {
		if v, err := s.VariableValue(ctx, path); err != nil || v != want {
			t.Errorf("%s: %q %v", path, v, err)
		}
	}
	if b, err := s.RawResource(ctx, "u/x/r"); err != nil || string(b) != "1" {
		t.Errorf("r: %s %v", b, err)
	}
	// A nil *Services has nothing.
	var none *Services
	if _, err := none.VariableValue(ctx, "u/x/v"); err == nil || err.Error() != "Not found: Variable u/x/v not found" {
		t.Errorf("nil: %v", err)
	}
	// A value jsonb refuses cannot be added.
	if err := bundle.SetResource("u/x/bad", Resource{Value: json.RawMessage(`"\u0000"`)}); err == nil {
		t.Error("a NUL escape was stored")
	}
}

// TestAPIEnv: the stored root is dropped when it equals the innermost
// root, the parent or the job itself (WA-V1, WA-V3).
func TestAPIEnv(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"WM_JOB_ID": "S", "WM_FLOW_JOB_ID": "F", "WM_ROOT_FLOW_JOB_ID": "F", "WM_ROOT_JOB_ID": "F"}, ""},
		{map[string]string{"WM_JOB_ID": "S", "WM_FLOW_JOB_ID": "L", "WM_ROOT_FLOW_JOB_ID": "L", "WM_ROOT_JOB_ID": "R"}, "R"},
		{map[string]string{"WM_JOB_ID": "R", "WM_ROOT_JOB_ID": "R"}, ""},
	} {
		got := APIEnv(c.env)
		if got["WM_ROOT_JOB_ID"] != c.want || c.env["WM_ROOT_JOB_ID"] == "" {
			t.Errorf("%v: %q, want %q", c.env, got["WM_ROOT_JOB_ID"], c.want)
		}
		if got["WM_JOB_ID"] != c.env["WM_JOB_ID"] {
			t.Errorf("%v: the other variables changed", c.env)
		}
	}
}
