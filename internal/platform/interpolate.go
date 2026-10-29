package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The interpolation walker of ADR 0007: resolution of $var:, $jsonvar:,
// $res: and the strings that name reserved variables, implemented once
// for the three contracts of WA-40, which it keeps apart:
//
//   - (a) the raw read (get_value, RawResource): no interpolation;
//   - (b) the client interpolated read (get_value_interpolated,
//     InterpolatedResource) of WA-12: without a job id, $WM_* strings stay
//     literal; with one they are that job's stored fields (APIEnv);
//   - (c) worker argument resolution (ResolveArgs) of JE-41, JE-42 and
//     WR-5: a leaf job's arguments before it runs, and flow_env (JE-38),
//     where a nested $res: is a read of contract (b) with the job's id,
//     which starts a depth counter of its own (WA-V3).

// maxDepth is the depth at which both walkers fail, before they look at
// the value there: the deepest node allowed is at depth 49 (WA-12,
// JE-42).
const maxDepth = 50

// Arrays are walked only at a depth of at most maxArrayDepth, and only
// when they have at most maxArrayItems items; any other array is kept as
// it is, and nothing inside it is visited (WA-12, JE-42).
const (
	maxArrayDepth = 2
	maxArrayItems = 1000
)

// The markers whose presence anywhere in an argument's text makes worker
// argument resolution walk it (JE-42 rule 1, WR-5).
var argMarkers = [][]byte{[]byte("$var:"), []byte("$jsonvar:"), []byte("$res:"), []byte("$encrypted:")}

// storedNode is a resource's value as the API reads it from storage: the
// jsonb column's text (key order, duplicates and numbers as jsonb keeps
// them, FX-42), parsed by the backend's JSON library. A NULL value is
// JSON null (WA-12).
func storedNode(value json.RawMessage) (*node, *Error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return &node{kind: kNull, text: "null"}, nil
	}
	stored, err := jsonval.FromProducer(value).Stored()
	if err != nil {
		return nil, internal("windmill-lite: the stored value cannot be read: %v", err)
	}
	n, err := parseJSON(stored)
	if err != nil {
		return nil, internal("windmill-lite: the stored value cannot be read: %v", err)
	}
	return n, nil
}

// RawResource answers a raw read of the resource at path (contract (a),
// resources/get_value): its value as stored, written out by the backend's
// JSON library, with nothing resolved.
func (s *Services) RawResource(ctx context.Context, path string) ([]byte, error) {
	r, ok, err := s.resource(ctx, path)
	switch {
	case err != nil:
		return nil, internal("%v", err)
	case !ok:
		return nil, notFound("Resource %s not found", path)
	}
	n, e := storedNode(r.Value)
	if e != nil {
		return nil, e
	}
	return n.write(nil), nil
}

// InterpolatedResource answers an interpolated read of the resource at
// path (contract (b), resources/get_value_interpolated, WA-12): its stored
// value with $var:, $jsonvar: and nested $res: strings resolved. job is
// the reserved variables of the job the read names (APIEnv), whose $WM_*
// strings it resolves, or nil for a read without a job id, which leaves
// them literal, as the stock clients' reads do. The error is an *Error
// with the API's status and body: a missing resource, a missing variable,
// an invalid nested path, a $jsonvar: that is not JSON, or the depth
// limit.
func (s *Services) InterpolatedResource(ctx context.Context, path string, job map[string]string) ([]byte, error) {
	n, e := (&apiWalk{s: s, ctx: ctx, job: job}).resource(path, 0, false)
	if e != nil {
		return nil, e
	}
	return n.write(nil), nil
}

// ResourceExists answers resources/exists.
func (s *Services) ResourceExists(ctx context.Context, path string) (bool, error) {
	_, ok, err := s.resource(ctx, path)
	if err != nil {
		return false, internal("%v", err)
	}
	return ok, nil
}

// VariableExists answers variables/exists.
func (s *Services) VariableExists(ctx context.Context, path string) (bool, error) {
	_, ok, err := s.variable(ctx, path)
	if err != nil {
		return false, internal("%v", err)
	}
	return ok, nil
}

// VariableValue answers variables/get_value (WA-11): the variable's value,
// a secret's plaintext as Secrets reveals it and the value resolvers
// leave it. A missing variable is the 404 `Not found: Variable <p> not
// found`; a secret no resolver supplies fails with secret_unavailable.
func (s *Services) VariableValue(ctx context.Context, path string) (string, error) {
	v, e := s.variableValue(ctx, path)
	if e != nil {
		return "", e
	}
	return v, nil
}

func (s *Services) variableValue(ctx context.Context, path string) (string, *Error) {
	v, ok, err := s.variable(ctx, path)
	switch {
	case err != nil:
		return "", internal("%v", err)
	case !ok:
		return "", notFound("Variable %s not found", path)
	case !v.IsSecret:
		return v.Value, nil
	}
	plain, err := s.secrets().Reveal(ctx, path, v)
	if err != nil {
		return "", internal("%v", err)
	}
	if len(s.resolvers()) == 0 {
		return plain, nil
	}
	out, e := s.resolve(ctx, path, false, stringNode(plain))
	if e != nil {
		return "", e
	}
	if out.kind != kString {
		return "", internal("windmill-lite: a value resolver replaced the secret variable %s with a value that is not a string", path)
	}
	return out.text, nil
}

// resolve passes a value through the host's value resolvers, in order.
func (s *Services) resolve(ctx context.Context, path string, resource bool, n *node) (*node, *Error) {
	for _, r := range s.resolvers() {
		out, err := r.ResolveValue(ctx, path, resource, jsonval.FromProducer(n.write(nil)))
		if err != nil {
			return nil, internal("%v", err)
		}
		if n, err = parseJSON(out.Producer()); err != nil {
			return nil, internal("windmill-lite: a value resolver answered %s with text that is not JSON: %v", path, err)
		}
	}
	return n, nil
}

// apiWalk is one read of contract (b).
type apiWalk struct {
	s   *Services
	ctx context.Context
	job map[string]string
}

// resource loads and interpolates the resource at path, at depth: 0 for
// the read's own resource, whose absence is the direct not-found of
// WA-42, or the depth of the $res: string that names it plus one, whose
// absence is the not-found of a nested reference (written without
// Windmill's Rust location, D14).
func (w *apiWalk) resource(path string, depth int, nested bool) (*node, *Error) {
	r, ok, err := w.s.resource(w.ctx, path)
	switch {
	case err != nil:
		return nil, internal("%v", err)
	case !ok && nested:
		return nil, notFound("Resource not found at name %s", path)
	case !ok:
		return nil, notFound("Resource %s not found", path)
	}
	n, e := storedNode(r.Value)
	if e != nil {
		return nil, e
	}
	if n, e = w.walk(n, depth); e != nil {
		return nil, e
	}
	if len(w.s.resolvers()) == 0 {
		return n, nil
	}
	return w.s.resolve(w.ctx, path, true, n)
}

func (w *apiWalk) walk(n *node, depth int) (*node, *Error) {
	if depth >= maxDepth {
		return nil, internal("Maximum resource/variable interpolation depth (50) exceeded; this usually indicates a circular `$res:` or `$var:` reference")
	}
	switch n.kind {
	case kString:
		s := n.text
		switch {
		case strings.HasPrefix(s, "$var:"):
			v, e := w.s.variableValue(w.ctx, s[len("$var:"):])
			if e != nil {
				return nil, e
			}
			return stringNode(v), nil
		case strings.HasPrefix(s, "$jsonvar:"):
			v, e := w.s.variableValue(w.ctx, s[len("$jsonvar:"):])
			if e != nil {
				return nil, e
			}
			parsed, err := parseJSON([]byte(v))
			if err != nil {
				return nil, internal("Failed to parse $jsonvar value as JSON: %v", err)
			}
			return parsed, nil
		case strings.HasPrefix(s, "$res:"):
			p := s[len("$res:"):]
			if len(strings.Split(p, "/")) < 2 {
				return nil, internal("Invalid resource path: %s", p)
			}
			return w.resource(p, depth+1, true)
		case strings.HasPrefix(s, "$WM_") && w.job != nil:
			if v, ok := w.job[s[1:]]; ok {
				return stringNode(v), nil
			}
		}
		return n, nil
	case kObject:
		out := &node{kind: kObject, keys: n.keys, vals: make([]*node, len(n.vals))}
		for i, v := range n.vals {
			var e *Error
			if out.vals[i], e = w.walk(v, depth+1); e != nil {
				return nil, e
			}
		}
		return out, nil
	case kArray:
		if depth > maxArrayDepth || len(n.items) > maxArrayItems {
			return n, nil
		}
		out := &node{kind: kArray, items: make([]*node, len(n.items))}
		for i, v := range n.items {
			var e *Error
			if out.items[i], e = w.walk(v, depth+1); e != nil {
				return nil, e
			}
		}
		return out, nil
	}
	return n, nil
}

// APIEnv returns the reserved variables that an interpolated read with a
// job's id resolves $WM_* strings from, for the job whose process
// environment is env (WA-V3): the same, except WM_ROOT_JOB_ID, which is
// the job's stored root rather than its effective root. The queue drops a
// stored root equal to the innermost root, the parent or the job itself
// (WA-V1), and an absent one is "", so a step directly in a top-level flow
// sees "" there while its process sees the flow.
func APIEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	if root := env["WM_ROOT_JOB_ID"]; root == env["WM_ROOT_FLOW_JOB_ID"] || root == env["WM_FLOW_JOB_ID"] || root == env["WM_JOB_ID"] {
		out["WM_ROOT_JOB_ID"] = ""
	}
	return out
}

// ResolveArgs is worker argument resolution (contract (c), JE-41, JE-42,
// WR-5): the arguments a leaf job's script receives, from its stored
// arguments, a JSON object. An argument whose text holds none of $var:,
// $jsonvar:, $res: and $encrypted: is kept as it is. Any other is parsed,
// walked and written out again by the backend's JSON library (FX-42 rule
// 2): a string that starts with a marker becomes the variable (a string),
// the variable parsed as JSON, the resource's interpolated value (a read
// of contract (b) with apiEnv, the job's APIEnv), or the decrypted
// $encrypted: value; a string $<NAME> becomes env[NAME], the job's
// reserved variables, when env has it; and nothing a substitution produced
// is walked again. The members keep their order. flow_env is resolved the
// same way (JE-38).
//
// The error is an *Error, which fails the job before its script runs:
// “Variable <p> not found for `<name>`: <cause>“, “Resource <p> not
// found for `<name>`: <cause>“ (cause the API's error text), an invalid
// $res: path, a $jsonvar: that is not JSON, or the depth limit, each
// naming the innermost object key or the argument.
func (s *Services) ResolveArgs(ctx context.Context, args jsonval.Value, env, apiEnv map[string]string) (jsonval.Value, error) {
	text := args.Producer()
	if !hasMarker(text) {
		return args, nil
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return jsonval.Value{}, fmt.Errorf("platform: the arguments are not a JSON object")
	}
	w := &workerWalk{s: s, ctx: ctx, env: env, apiEnv: apiEnv}
	out := []byte{'{'}
	for i := 0; dec.More(); i++ {
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return jsonval.Value{}, fmt.Errorf("platform: the arguments are not a JSON object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return jsonval.Value{}, fmt.Errorf("platform: the arguments are not a JSON object: %w", err)
		}
		if i > 0 {
			out = append(out, ',')
		}
		out = appendString(out, key)
		out = append(out, ':')
		if !hasMarker(raw) {
			out = append(out, raw...)
			continue
		}
		n, err := parseJSON(raw)
		if err != nil {
			return jsonval.Value{}, fmt.Errorf("platform: argument %s: %w", key, err)
		}
		r, e := w.walk(key, n, 0)
		if e != nil {
			return jsonval.Value{}, e
		}
		out = r.write(out)
	}
	if _, err := dec.Token(); err != nil {
		return jsonval.Value{}, fmt.Errorf("platform: the arguments are not a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return jsonval.Value{}, fmt.Errorf("platform: the arguments are not a JSON object")
	}
	return jsonval.FromProducer(append(out, '}')), nil
}

func hasMarker(text []byte) bool {
	for _, m := range argMarkers {
		if bytes.Contains(text, m) {
			return true
		}
	}
	return false
}

// workerWalk is one resolution of contract (c).
type workerWalk struct {
	s           *Services
	ctx         context.Context
	env, apiEnv map[string]string
}

// walk resolves n, which is at depth below the argument and whose
// innermost object key (or the argument itself) is name.
func (w *workerWalk) walk(name string, n *node, depth int) (*node, *Error) {
	if depth >= maxDepth {
		return nil, internal("Maximum resource/variable interpolation depth (50) exceeded for `%s`; this usually indicates a circular `$res:` or `$var:` reference", name)
	}
	switch n.kind {
	case kString:
		s := n.text
		switch {
		case strings.HasPrefix(s, "$var:"):
			p := s[len("$var:"):]
			v, e := w.s.variableValue(w.ctx, p)
			if e != nil {
				return nil, notFound("Variable %s not found for `%s`: %s", p, name, e.Body())
			}
			return stringNode(v), nil
		case strings.HasPrefix(s, "$jsonvar:"):
			p := s[len("$jsonvar:"):]
			v, e := w.s.variableValue(w.ctx, p)
			if e != nil {
				return nil, notFound("Variable %s not found for `%s`: %s", p, name, e.Body())
			}
			parsed, err := parseJSON([]byte(v))
			if err != nil {
				return nil, internal("Failed to parse $jsonvar value as JSON: %v", err)
			}
			return parsed, nil
		case strings.HasPrefix(s, "$res:"):
			p := s[len("$res:"):]
			if len(strings.Split(p, "/")) < 2 && !strings.HasPrefix(p, "CUSTOM_INSTANCE_DB/") {
				return nil, internal("Argument `%s` is an invalid resource path: %s", name, p)
			}
			r, e := (&apiWalk{s: w.s, ctx: w.ctx, job: w.apiEnv}).resource(p, 0, false)
			if e != nil {
				return nil, notFound("Resource %s not found for `%s`: %s", p, name, e.Body())
			}
			return r, nil
		case strings.HasPrefix(s, "$encrypted:"):
			plain, err := w.s.secrets().Decrypt(w.ctx, s[len("$encrypted:"):])
			if err != nil {
				var pe *Error
				if errors.As(err, &pe) {
					return nil, pe
				}
				return nil, internal("%v", err)
			}
			parsed, err := parseJSON([]byte(plain))
			if err != nil {
				return nil, internal("windmill-lite: the decrypted $encrypted: value of `%s` is not JSON: %v", name, err)
			}
			return parsed, nil
		case strings.HasPrefix(s, "$"):
			if v, ok := w.env[s[1:]]; ok {
				return stringNode(v), nil
			}
		}
		return n, nil
	case kObject:
		out := &node{kind: kObject, keys: n.keys, vals: make([]*node, len(n.vals))}
		for i, v := range n.vals {
			var e *Error
			if out.vals[i], e = w.walk(n.keys[i], v, depth+1); e != nil {
				return nil, e
			}
		}
		return out, nil
	case kArray:
		if depth > maxArrayDepth || len(n.items) > maxArrayItems {
			return n, nil
		}
		out := &node{kind: kArray, items: make([]*node, len(n.items))}
		for i, v := range n.items {
			var e *Error
			if out.items[i], e = w.walk(name, v, depth+1); e != nil {
				return nil, e
			}
		}
		return out, nil
	}
	return n, nil
}
