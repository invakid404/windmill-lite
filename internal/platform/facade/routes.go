package facade

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// routes.json is the route table of Windmill's REST API: every operation
// of the Apache-2.0 openapi.yaml vendored in internal/oracle/openapi (its
// method, path template and operationId), generated from it by
// TestRouteTable with -update, which also fails while the two differ.
//
//go:embed routes.json
var routesJSON []byte

// Route is one operation of the API: its method, its path template
// relative to the API base (OpenAPI.BASE, <origin>/api), and its
// operationId, which the facade's handlers are registered by.
type Route struct {
	Method    string
	Template  string
	Operation string
	segs      []string
	literals  int
}

// Name is the route as a 501 names it, "<METHOD> <template>" with the
// workspace parameter written {ws} as the reference writes it (WA-41/a,
// WA-V5): for example "GET /w/{ws}/jobs_u/get/{id}".
func (r *Route) Name() string {
	return r.Method + " " + strings.ReplaceAll(r.Template, "{workspace}", "{ws}")
}

// routeTable holds the routes, by method, each list most specific first.
type routeTable struct {
	byMethod map[string][]*Route
	byOp     map[string]*Route
}

func loadRoutes(data []byte) (*routeTable, error) {
	var doc struct {
		Routes [][3]string `json:"routes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	t := &routeTable{byMethod: map[string][]*Route{}, byOp: map[string]*Route{}}
	for _, r := range doc.Routes {
		rt := &Route{Method: r[0], Template: r[1], Operation: r[2], segs: strings.Split(strings.TrimPrefix(r[1], "/"), "/")}
		for _, s := range rt.segs {
			if !isParam(s) {
				rt.literals++
			}
		}
		if _, dup := t.byOp[rt.Operation]; dup {
			return nil, fmt.Errorf("routes: operation %s appears twice", rt.Operation)
		}
		t.byOp[rt.Operation] = rt
		t.byMethod[rt.Method] = append(t.byMethod[rt.Method], rt)
	}
	for _, rs := range t.byMethod {
		// More literal segments first, so a literal beats a parameter
		// where both match; then fewer segments, so an exact match beats
		// a trailing parameter that swallows several; then by template,
		// so the order never depends on the file's.
		sort.SliceStable(rs, func(i, j int) bool {
			a, b := rs[i], rs[j]
			if a.literals != b.literals {
				return a.literals > b.literals
			}
			if len(a.segs) != len(b.segs) {
				return len(a.segs) < len(b.segs)
			}
			return a.Template < b.Template
		})
	}
	return t, nil
}

func isParam(seg string) bool {
	return len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}'
}

var defaultRoutes = func() *routeTable {
	t, err := loadRoutes(routesJSON)
	if err != nil {
		panic(err)
	}
	return t
}()

// match finds the route for method and an API path (the request path
// without the /api prefix, still escaped), and its path parameters,
// unescaped. A parameter matches one segment, except the template's last
// one, which may take the rest of the path, slashes included: Windmill's
// paths (u/x/script) travel unescaped, since the stock client encodes
// path parameters with encodeURI. Among the routes that match, the one
// with the most literal segments wins.
func (t *routeTable) match(method, escapedPath string) (*Route, map[string]string, bool) {
	segs := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	for _, r := range t.byMethod[method] {
		if params, ok := r.bind(segs); ok {
			return r, params, true
		}
	}
	return nil, nil, false
}

func (r *Route) bind(segs []string) (map[string]string, bool) {
	n := len(r.segs)
	if len(segs) < n || (len(segs) > n && !isParam(r.segs[n-1])) {
		return nil, false
	}
	params := map[string]string{}
	for i, s := range r.segs {
		v := segs[i]
		if i == n-1 && len(segs) > n {
			v = strings.Join(segs[i:], "/")
		}
		if !isParam(s) {
			if s != v {
				return nil, false
			}
			continue
		}
		u, err := url.PathUnescape(v)
		if err != nil || u == "" {
			return nil, false
		}
		params[s[1:len(s)-1]] = u
	}
	return params, true
}
