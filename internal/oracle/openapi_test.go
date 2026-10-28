package oracle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The oracle client is written against the Apache-2.0 REST schema of
// Windmill v1.767.0, vendored in openapi/ (LIC-1). This test calls every
// method of Client against a server that records the requests, and
// requires each request to match an operation of that schema: the method,
// the path template, and, for a JSON body, only properties the operation's
// request schema declares.

type apiOperation struct {
	method, template string
	re               *regexp.Regexp
	bodyProps        map[string]bool // nil: any properties
}

func loadOperations(t *testing.T) []apiOperation {
	t.Helper()
	data, err := os.ReadFile("openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	// QuoteMeta escapes the braces of a {param}.
	param := regexp.MustCompile(`\\\{([a-z_]+)\\\}`)
	var ops []apiOperation
	for tmpl, methods := range doc.Paths {
		pattern := param.ReplaceAllStringFunc(regexp.QuoteMeta(tmpl), func(m string) string {
			if m == `\{path\}` {
				return `.+` // Windmill item paths keep their slashes
			}
			return `[^/]+`
		})
		re := regexp.MustCompile("^" + pattern + "$")
		for m, node := range methods {
			if m == "parameters" {
				continue
			}
			var op struct {
				RequestBody struct {
					Content map[string]struct {
						Schema yaml.Node `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"requestBody"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatal(err)
			}
			var props map[string]bool
			if c, ok := op.RequestBody.Content["application/json"]; ok {
				props = schemaProps(t, c.Schema, doc.Components.Schemas)
			}
			ops = append(ops, apiOperation{method: strings.ToUpper(m), template: tmpl, re: re, bodyProps: props})
		}
	}
	// Prefer the most specific template: literal segments over parameters.
	sort.Slice(ops, func(i, j int) bool {
		return strings.Count(ops[i].template, "{") < strings.Count(ops[j].template, "{")
	})
	return ops
}

// schemaProps returns the declared properties of a request schema, or nil
// when it admits any (no properties, or additionalProperties).
func schemaProps(t *testing.T, n yaml.Node, schemas map[string]yaml.Node) map[string]bool {
	t.Helper()
	var s struct {
		Ref        string               `yaml:"$ref"`
		Properties map[string]yaml.Node `yaml:"properties"`
	}
	if err := n.Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		target, ok := schemas[name]
		if !ok {
			t.Fatalf("unresolved %s", s.Ref)
		}
		return schemaProps(t, target, schemas)
	}
	if len(s.Properties) == 0 {
		return nil
	}
	props := map[string]bool{}
	for k := range s.Properties {
		props[k] = true
	}
	return props
}

type seenRequest struct {
	method, path string
	body         []byte
}

func TestClientUsesTheOpenAPISurface(t *testing.T) {
	ops := loadOperations(t)
	var mu sync.Mutex
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenRequest{r.Method, strings.TrimPrefix(r.URL.Path, "/api"), body})
		mu.Unlock()
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && (strings.HasSuffix(p, "/auth/login") || strings.Contains(p, "/queue/cancel/")):
			io.WriteString(w, "tok")
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, "00000000-0000-4000-8000-000000000001")
		case strings.HasSuffix(p, "/version"):
			io.WriteString(w, OracleVersion)
		case strings.HasSuffix(p, "/list"):
			io.WriteString(w, "[]")
		case strings.Contains(p, "get_result_maybe"):
			io.WriteString(w, `{"completed":true,"result":1}`)
		case strings.Contains(p, "/jobs_u/get/"):
			io.WriteString(w, `{"type":"CompletedJob","id":"x","success":true}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c := &Client{BaseURL: srv.URL, Token: "tok"}
	const ws, path, id = "wl-abc-1", "f/wl_smoke/two_step", "00000000-0000-4000-8000-000000000001"
	calls := map[string]func() error{
		"Version":         func() error { _, err := c.Version(ctx); return err },
		"Health":          func() error { _, err := c.Health(ctx); return err },
		"Login":           func() error { _, err := c.Login(ctx, AdminEmail, AdminPassword); return err },
		"CreateWorkspace": func() error { return c.CreateWorkspace(ctx, ws) },
		"DeleteWorkspace": func() error { return c.DeleteWorkspace(ctx, ws) },
		"CreateFolder":    func() error { return c.CreateFolder(ctx, ws, "t") },
		"CreateScript": func() error {
			_, err := c.CreateScript(ctx, ws, NewScript{Path: "f/t/s", Summary: "s", Content: "x", Language: "bun"})
			return err
		},
		"CreateFlow": func() error {
			return c.CreateFlow(ctx, ws, json.RawMessage(`{"path":"f/t/x","summary":"x","value":{"modules":[]}}`))
		},
		"QueueCount":             func() error { _, err := c.QueueCount(ctx, ws); return err },
		"ListScripts":            func() error { _, err := c.ListScripts(ctx, ws); return err },
		"ListFlows":              func() error { _, err := c.ListFlows(ctx, ws); return err },
		"ScriptDeploymentStatus": func() error { _, err := c.ScriptDeploymentStatus(ctx, ws, "abc123"); return err },
		"FlowDeploymentStatus":   func() error { _, err := c.FlowDeploymentStatus(ctx, ws, path); return err },
		"GetFlow":                func() error { _, err := c.GetFlow(ctx, ws, path); return err },
		"ResultMaybe":            func() error { _, err := c.ResultMaybe(ctx, ws, id); return err },
		"RunFlow":                func() error { _, err := c.RunFlow(ctx, ws, path, json.RawMessage(`{"x":3}`), id); return err },
		"RunScript": func() error {
			_, err := c.RunScript(ctx, ws, "f/wl_smoke/double", json.RawMessage(`{"x":3}`), id)
			return err
		},
		"Cancel":             func() error { return c.Cancel(ctx, ws, id, "test") },
		"GetJob":             func() error { _, err := c.GetJob(ctx, ws, id); return err },
		"GetArgs":            func() error { _, err := c.GetArgs(ctx, ws, id); return err },
		"CreateVariable":     func() error { return c.CreateVariable(ctx, ws, "f/x/v", "v", false, "") },
		"CreateResource":     func() error { return c.CreateResource(ctx, ws, "f/x/r", "t", json.RawMessage(`{"a":1}`), "") },
		"CreateResourceType": func() error { return c.CreateResourceType(ctx, ws, "t", json.RawMessage(`{"type":"object"}`), "") },
	}
	// Every exported method of Client that calls the API is in calls.
	ct := reflect.TypeOf(c)
	for i := 0; i < ct.NumMethod(); i++ {
		if _, ok := calls[ct.Method(i).Name]; !ok {
			t.Errorf("Client.%s is not exercised by this test", ct.Method(i).Name)
		}
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, r := range seen {
		var op *apiOperation
		for i := range ops {
			if ops[i].method == r.method && ops[i].re.MatchString(r.path) {
				op = &ops[i]
				break
			}
		}
		if op == nil {
			t.Errorf("%s %s is not an operation of openapi/openapi.yaml", r.method, r.path)
			continue
		}
		if len(r.body) == 0 || op.bodyProps == nil {
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Errorf("%s %s: body %s: %v", r.method, r.path, r.body, err)
			continue
		}
		for k := range body {
			if !op.bodyProps[k] {
				t.Errorf("%s %s (%s): body property %q is not in the request schema", r.method, r.path, op.template, k)
			}
		}
	}
	if len(seen) < len(calls) {
		t.Fatalf("%d requests for %d calls", len(seen), len(calls))
	}
}
