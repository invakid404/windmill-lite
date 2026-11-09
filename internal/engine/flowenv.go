package engine

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// envStore decides what the input transforms of one module's transition
// store when their text contains flow_env (JE-38, Windmill 1.855.0):
//
//  1. An exact read of a key the env defines, flow_env.<key> or
//     flow_env["<key>"] (JE-7's shortcut), stores the entry as written in
//     the env, unresolved; the script job resolves it (JE-41).
//  2. Into a subflow called by path (a flow module, which never runs as a
//     collapsed loop body, CF-8), only a secret entry is kept this way.
//  3. Any other text is evaluated with the resolved env, and stored
//     sealed under the root job's key (platform.SealForRoot) when it reads
//     a secret entry by name (readsByName).
//
// Which entries are secret is looked up once, at the first transform that
// needs it.
type envStore struct {
	r *run
	// src is flow_env as stored for the module's scope, or zero.
	src jsonval.Value
	// subflow is true for a flow module, a host-provided one included.
	subflow bool
	// seal is false at a host-provided site, whose provider runs outside
	// the engine and could not open a sealed value: step 3 stores it in
	// clear there.
	seal bool
	// secret holds the keys of the secret entries once looked up.
	secret map[string]bool
}

// envStore returns the decisions for the transforms of module m of fr.
func (r *run) envStore(fr *frame, m *ir.Module) *envStore {
	target := m.Kind
	if m.Provided != nil {
		target = m.Provided.Target.Kind
	}
	return &envStore{
		r:       r,
		src:     fr.owner().envSrc,
		subflow: target == ir.KindSubflow,
		seal:    m.Kind != ir.KindProvided,
	}
}

// kept returns the entry an exact read of x stores, and true, when steps 1
// and 2 keep it.
func (e *envStore) kept(ctx context.Context, x *expr.Expr) (jsonval.Value, bool) {
	k := x.Shortcut.FlowKey
	if k == nil || !k.Env || e.src.IsZero() {
		return jsonval.Value{}, false
	}
	entry, ok := envEntry(e.src, k.Key)
	if !ok || e.subflow && !e.secrets(ctx)[k.Key] {
		return jsonval.Value{}, false
	}
	return jsonval.FromProducer(entry), true
}

// stored returns what x stores once evaluated to v (step 3): v, or v
// sealed under the run's root job's key when x reads a secret entry by
// name.
func (e *envStore) stored(ctx context.Context, x *expr.Expr, v jsonval.Value) (jsonval.Value, error) {
	if !e.seal || e.src.IsZero() {
		return v, nil
	}
	for key, secret := range e.secrets(ctx) {
		if !secret || !readsByName(x.Text, key) {
			continue
		}
		sealed, err := platform.SealForRoot(e.r.rootID, v.Producer())
		if err != nil {
			return jsonval.Value{}, err
		}
		return jsonval.FromProducer(jsonString(sealed)), nil
	}
	return v, nil
}

// secrets looks up which of the env's entries are secret, once. An entry
// whose lookup fails is taken as secret, so that the failure never puts a
// secret in clear into the stored arguments; the leaf's own resolution of
// the reference then reports what failed.
func (e *envStore) secrets(ctx context.Context) map[string]bool {
	if e.secret != nil {
		return e.secret
	}
	e.secret = map[string]bool{}
	ms, _ := members(e.src.Producer())
	for _, m := range ms {
		secret, err := e.r.platform.SecretEntry(ctx, m.value)
		e.secret[m.key] = secret || err != nil
	}
	return e.secret
}

// envEntry returns the stored text of the env's entry key, the last of
// duplicate keys as jsonb keeps it.
func envEntry(env jsonval.Value, key string) ([]byte, bool) {
	ms, ok := members(env.Producer())
	if !ok {
		return nil, false
	}
	var entry []byte
	found := false
	for _, m := range ms {
		if m.key == key {
			entry, found = m.value, true
		}
	}
	return entry, found
}

// readsByName reports whether the text reads flow_env's entry key by name
// (JE-38): flow_env.<key> or flow_env?.<key>, with no letter, digit, _ or $
// just before flow_env or just after the key; or flow_env[…] or
// flow_env?.[…] with the key in double, single or back quotes, with no
// such character before flow_env. The test reads only the text, so a
// computed key or a use of the whole object is not seen.
//
// An empty key, which an env may define, follows the same rule:
// flow_env[""] reads it, and so does a dot form followed by any other
// character, such as flow_env?.["k"], which the text test cannot tell
// apart; that seals more than the read needs, never less.
func readsByName(text, key string) bool {
	for at := 0; ; {
		i := strings.Index(text[at:], "flow_env")
		if i < 0 {
			return false
		}
		start, rest := at+i, text[at+i+len("flow_env"):]
		at = start + len("flow_env")
		if r, _ := utf8.DecodeLastRuneInString(text[:start]); start > 0 && identRune(r) {
			continue
		}
		for _, dot := range []string{".", "?."} {
			if after, ok := strings.CutPrefix(rest, dot+key); ok {
				if r, _ := utf8.DecodeRuneInString(after); after == "" || !identRune(r) {
					return true
				}
			}
		}
		for _, open := range []string{"[", "?.["} {
			for _, q := range []string{`"`, `'`, "`"} {
				if strings.HasPrefix(rest, open+q+key+q+"]") {
					return true
				}
			}
		}
	}
}

// identRune is a letter, a digit, _ or $: a rune that would make the
// identifier around it longer.
func identRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
