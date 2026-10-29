package facade

import (
	"encoding/json"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The resource and variable reads (milestone PF2) answer from
// Config.Platform with Windmill's statuses, content types and bodies, the
// same on both transports (WA-10 to WA-12, WA-40, WA-42/b and /c, WA-V3/a).
func TestProviderRoutes(t *testing.T) {
	store := platform.NewStore()
	store.SetVariable("u/x/pw", platform.Variable{Value: "s3cret"})
	store.SetVariable("u/x/q", platform.Variable{Value: "a\"b\n"})
	for path, v := range map[string]string{
		"u/x/db":         `{"password":"$var:u/x/pw","tok":"$WM_TOKEN"}`,
		"u/t/outer":      `{"x":"$res:u/t/missing"}`,
		"u/test/context": `{"root":"$WM_ROOT_JOB_ID","job":"$WM_JOB_ID"}`,
	} {
		if err := store.SetResource(path, platform.Resource{Type: "object", Value: json.RawMessage(v)}); err != nil {
			t.Fatal(err)
		}
	}
	f := newFacade(t, Config{Platform: &platform.Services{Resources: store, Variables: store}})
	// A direct step of top-level flow F: its stored root is "".
	f.Register("inv-a", "F", map[string]string{"WM_JOB_ID": "S", "WM_FLOW_JOB_ID": "F", "WM_ROOT_FLOW_JOB_ID": "F", "WM_ROOT_JOB_ID": "F", "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws"})
	const base = "/api/w/ws/"
	for _, c := range []struct {
		spec, path string
		status     int
		ctype      string
		body       string
	}{
		{"WA-12/a", "resources/get_value_interpolated/u/x/db", 200, "application/json", `{"tok":"$WM_TOKEN","password":"s3cret"}`},
		{"WA-40", "resources/get_value/u/x/db", 200, "application/json", `{"tok":"$WM_TOKEN","password":"$var:u/x/pw"}`},
		{"WA-40", "resources/exists/u/x/db", 200, "application/json", `true`},
		{"WA-40", "resources/exists/u/x/none", 200, "application/json", `false`},
		{"WA-11/a", "variables/get_value/u/x/pw", 200, "application/json", `"s3cret"`},
		{"WA-11", "variables/get_value/u/x/q", 200, "application/json", `"a\"b\n"`},
		{"WA-11/a", "variables/get_value/$var:u/x/pw", 404, "text/plain", `Not found: Variable $var:u/x/pw not found`},
		{"WA-40", "variables/exists/u/x/pw", 200, "application/json", `true`},
		{"WA-42/b", "resources/get_value_interpolated/u/x/none", 404, "text/plain", `Not found: Resource u/x/none not found`},
		{"WA-42/c", "resources/get_value_interpolated/u/t/outer", 404, "text/plain", `Not found: Resource not found at name u/t/missing`},
		{"WA-V3/a", "resources/get_value_interpolated/u/test/context", 200, "application/json", `{"job":"$WM_JOB_ID","root":"$WM_ROOT_JOB_ID"}`},
		// With the job's id, $WM_* strings are its stored fields: the
		// stored root of a direct step is "".
		{"WA-V3", "resources/get_value_interpolated/u/test/context?job_id=S", 200, "application/json", `{"job":"S","root":""}`},
		// A job the facade does not know of.
		{"WA-12", "resources/get_value_interpolated/u/test/context?job_id=nope", 501, "text/plain", UnsupportedPrefix + "GET /w/{ws}/resources/get_value_interpolated/{path}"},
	} {
		t.Run(c.spec+" "+c.path, func(t *testing.T) {
			rep := intercept(f, "inv-a", "GET", "http://x"+base+c.path, nil, nil)
			if rep.Status != c.status || rep.Headers["content-type"] != c.ctype || string(rep.Body) != c.body {
				t.Errorf("intercepted: %d %q %s, want %d %q %s", rep.Status, rep.Headers["content-type"], rep.Body, c.status, c.ctype, c.body)
			}
			if code, body := get(t, f.FreshOrigin()+base+c.path, "tok-a"); code != c.status || body != c.body {
				t.Errorf("TCP: %d %s, want %d %s", code, body, c.status, c.body)
			}
		})
	}
	// A write is `later`: 501.
	rep := intercept(f, "inv-a", "POST", "http://x"+base+"variables/create", nil, []byte(`{}`))
	if rep.Status != 501 || string(rep.Body) != UnsupportedPrefix+"POST /w/{ws}/variables/create" {
		t.Errorf("create: %d %s", rep.Status, rep.Body)
	}
}

// A facade with no platform answers every read as not found.
func TestProviderRoutesWithoutPlatform(t *testing.T) {
	f := newFacade(t, Config{})
	f.Register("inv-a", "F", env("S", "F", "tok-a"))
	rep := intercept(f, "inv-a", "GET", "http://x/api/w/ws/variables/get_value/u/x/v", nil, nil)
	if rep.Status != 404 || string(rep.Body) != "Not found: Variable u/x/v not found" {
		t.Fatalf("%d %s", rep.Status, rep.Body)
	}
}
