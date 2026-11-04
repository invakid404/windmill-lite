package facade

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// viewOf renders each job as the run that holds it and the job's id.
type viewOf string

func (v viewOf) JobRecord(id string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"run":%q,"id":%q}`, string(v), id)), nil
}

func (v viewOf) JobArgs(string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"run": %q}`, string(v))), nil
}

// The job reads ([WA-19]): GET jobs_u/get/{id} and jobs_u/get_args/{id}
// answer a job of a live run from the run that holds it, as JSON, a step
// that has ended included; the id is read as Windmill's UUID parser reads
// it. An id that names no job, and a job looked up in a workspace other
// than the caller's, answer Windmill's 404 texts without the location; an
// id that is not a UUID the web framework's 400. Once the run has ended,
// its jobs answer 501, which names the reason and counts for the 501
// policy (D22), while the root lookup still answers for them.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
func TestJobReadRoutes(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	run := uid("run")
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run, Path: "f/x", Run: run, View: viewOf(run)})
	a := f.Register("inv-a", run, env("job-a", "run", "tok-a"))
	a.End()
	b := f.Register("inv-b", run, env("job-b", "run", "tok-b"))
	origin := f.WarmOrigin()
	read := func(inv, route, ws, id string) harness.PlatformReply {
		return intercept(f, inv, "GET", origin+"/api/w/"+ws+"/jobs_u/"+route+"/"+id+"?no_logs=true", nil, nil)
	}
	for _, c := range []struct{ route, body string }{
		{"get", `{"run":%q,"id":%q}`},
		{"get_args", `{"run": %q}`},
	} {
		for _, id := range []string{run, uid("job-a"), uid("job-b"), strings.ToUpper(uid("job-a"))} {
			want := fmt.Sprintf(c.body, run, strings.ToLower(id))
			if c.route == "get_args" {
				want = fmt.Sprintf(c.body, run)
			}
			rep := read("inv-b", c.route, "ws", id)
			if rep.Status != 200 || rep.Headers["content-type"] != "application/json" || string(rep.Body) != want {
				t.Errorf("%s %s: %d %v %s, want %s", c.route, id, rep.Status, rep.Headers, rep.Body, want)
			}
		}
	}
	unknown := "00000000-0000-4000-8000-000000000000"
	for _, c := range []struct{ route, text string }{{"get", "Not found: Job not found at name "}, {"get_args", "Not found: Job Args not found at name "}} {
		for _, req := range []struct{ what, ws, id, text string }{
			{"an unknown id", "ws", unknown, c.text + unknown},
			{"a job of the run in another workspace", "other", uid("job-a"), c.text + uid("job-a")},
		} {
			if rep := read("inv-b", c.route, req.ws, req.id); rep.Status != 404 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != req.text {
				t.Errorf("%s %s: %d %v %s", c.route, req.what, rep.Status, rep.Headers, rep.Body)
			}
		}
		const notUUID = "Invalid URL: Cannot parse `id` with value `abc`: UUID parsing failed: invalid length: found 3"
		if rep := read("inv-b", c.route, "ws", "abc"); rep.Status != 400 || rep.Headers["content-type"] != "text/plain; charset=utf-8" || string(rep.Body) != notUUID {
			t.Errorf("%s of an id that is not a UUID: %d %v %s", c.route, rep.Status, rep.Headers, rep.Body)
		}
	}
	if got := b.Unsupported(context.Background()); len(got) != 0 {
		t.Errorf("unsupported %q", got)
	}
	b.End()
	endRun()
	c := f.Register("inv-c", "run-c", env("job-c", "root-c", "tok-c"))
	defer c.End()
	for _, route := range []string{"get", "get_args"} {
		rep := read("inv-c", route, "ws", uid("job-a"))
		want := "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/" + route + "/{id}: the job's run has ended, and windmill-lite keeps no record of the jobs of a run that has ended"
		if rep.Status != 501 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != want {
			t.Errorf("%s of a job of a run that has ended: %d %v %s", route, rep.Status, rep.Headers, rep.Body)
		}
	}
	got := c.Unsupported(context.Background())
	if want := []string{"GET /w/{ws}/jobs_u/get/{id}", "GET /w/{ws}/jobs_u/get_args/{id}"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unsupported %q, want %q", got, want)
	}
	if rep := intercept(f, "inv-c", "GET", origin+"/api/w/ws/jobs_u/get_root_job_id/"+uid("job-a"), nil, nil); rep.Status != 200 || string(rep.Body) != `"`+run+`"` {
		t.Errorf("the root lookup of a job of a run that has ended: %d %s", rep.Status, rep.Body)
	}
}

// The jobs_u routes that windmill-lite does not serve still answer 501 and
// count for the 501 policy (ADR 0007): the job reads are served, and
// nothing else of the group is.
func TestUnservedJobRoutes(t *testing.T) {
	f := newFacade(t, Config{Platform: &platform.Services{}})
	s := f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	defer s.End()
	o := f.WarmOrigin() + "/api/w/ws/jobs_u/"
	id := uid("job-a")
	routes := []struct{ method, path, route string }{
		{"GET", "get_logs/" + id, "GET /w/{ws}/jobs_u/get_logs/{id}"},
		{"GET", "completed/get/" + id, "GET /w/{ws}/jobs_u/completed/get/{id}"},
		{"GET", "getupdate/" + id, "GET /w/{ws}/jobs_u/getupdate/{id}"},
		{"GET", "completed/get_timing/" + id, "GET /w/{ws}/jobs_u/completed/get_timing/{id}"},
		{"POST", "queue/cancel/" + id, "POST /w/{ws}/jobs_u/queue/cancel/{id}"},
	}
	var want []string
	for _, r := range routes {
		rep := intercept(f, "inv-a", r.method, o+r.path, nil, nil)
		if rep.Status != 501 || string(rep.Body) != UnsupportedPrefix+r.route {
			t.Errorf("%s %s: %d %s", r.method, r.path, rep.Status, rep.Body)
		}
		want = append(want, r.route)
	}
	if got := s.Unsupported(context.Background()); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unsupported %q, want %q", got, want)
	}
}
