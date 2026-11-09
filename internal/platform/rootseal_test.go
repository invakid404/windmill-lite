package platform

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// JE-38, JE-42: a value sealed for a root job is "$encrypted:" and
// standard padded base64 that holds no plaintext and differs every time;
// worker resolution opens it, parsed as JSON, for a job of that root (its
// WM_ROOT_JOB_ID) and for no other, whose job fails as one with a value
// Windmill cannot decrypt; a value windmill-lite did not seal still goes
// to the host's secret service.
func TestSealForRoot(t *testing.T) {
	ctx := context.Background()
	const plain = `{"pw":"hidden!","n":1}`
	a, err := SealForRoot("root-1", []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	b, err := SealForRoot("root-1", []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	blob, ok := strings.CutPrefix(a, "$encrypted:")
	if !ok || a == b {
		t.Fatalf("sealed %q and %q", a, b)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(blob)
	if err != nil || strings.Contains(string(raw), "hidden") || strings.Contains(blob, "hidden") {
		t.Fatalf("not opaque standard base64: %q %v", a, err)
	}

	s := &Services{}
	args := func(v string) jsonval.Value {
		return jsonval.FromProducer([]byte(`{"a":` + string(JSONString(v)) + `}`))
	}
	for _, sealed := range []string{a, b} {
		out, err := s.ResolveArgs(ctx, args(sealed), map[string]string{"WM_ROOT_JOB_ID": "root-1"}, nil)
		if err != nil || string(out.Producer()) != `{"a":{"pw":"hidden!","n":1}}` {
			t.Fatalf("own root: %s %v", out.Producer(), err)
		}
	}
	const cause = "Internal: Could not decrypt value. The value may have been encrypted with a different key."
	for _, env := range []map[string]string{{"WM_ROOT_JOB_ID": "root-2"}, {}, nil} {
		_, err := s.ResolveArgs(ctx, args(a), env, nil)
		var pe *Error
		if !errors.As(err, &pe) || pe.Body() != cause {
			t.Fatalf("root %v: %v", env, err)
		}
	}
	// A tampered value is not windmill-lite's either.
	tampered := []byte(raw)
	tampered[len(tampered)-1] ^= 1
	if _, err := s.ResolveArgs(ctx, args("$encrypted:"+base64.StdEncoding.EncodeToString(tampered)), map[string]string{"WM_ROOT_JOB_ID": "root-1"}, nil); err == nil {
		t.Fatal("a tampered value opened")
	}
	// The host's service decrypts what windmill-lite did not seal, and
	// sees nothing windmill-lite opened.
	s.Secrets = hostSecrets{}
	out, err := s.ResolveArgs(ctx, jsonval.FromProducer([]byte(`{"a":"$encrypted:[1]","b":`+string(JSONString(a))+`}`)), map[string]string{"WM_ROOT_JOB_ID": "root-1"}, nil)
	if err != nil || string(out.Producer()) != `{"a":[1],"b":{"pw":"hidden!","n":1}}` {
		t.Fatalf("host and own: %s %v", out.Producer(), err)
	}
}

// JE-38: an entry of flow_env is secret when it is a literal $encrypted:
// string, or a $var: or $jsonvar: reference to a variable stored as
// secret; a $res: reference, a reference to a variable that is not secret
// or does not exist, a literal and a value that is not a string are not.
// A variable service's failure is the lookup's.
func TestSecretEntry(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	store.SetVariable("u/x/plain", Variable{Value: "p"})
	store.SetVariable("u/x/secret", Variable{Value: "s", IsSecret: true})
	s := &Services{Variables: store, Resources: store}
	for entry, want := range map[string]bool{
		`"$encrypted:AAAA"`:       true,
		`"$var:u/x/secret"`:       true,
		`"$jsonvar:u/x/secret"`:   true,
		`"$var:u/x/plain"`:        false,
		`"$jsonvar:u/x/plain"`:    false,
		`"$var:u/x/missing"`:      false,
		`"$res:u/x/secret"`:       false,
		`"x $var:u/x/secret"`:     false,
		`"lit"`:                   false,
		`1`:                       false,
		`["$var:u/x/secret"]`:     false,
		`{"a":"$var:u/x/secret"}`: false,
	} {
		got, err := s.SecretEntry(ctx, []byte(entry))
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", entry, got, err, want)
		}
	}
	if got, err := (*Services)(nil).SecretEntry(ctx, []byte(`"$var:u/x/secret"`)); err != nil || got {
		t.Errorf("no services: %v %v", got, err)
	}
	s.Variables = failingVariables{}
	if _, err := s.SecretEntry(ctx, []byte(`"$var:u/x/secret"`)); err == nil {
		t.Error("a failed lookup gave no error")
	}
}

type failingVariables struct{}

func (failingVariables) Variable(context.Context, string) (Variable, bool, error) {
	return Variable{}, false, errors.New("vault down")
}
