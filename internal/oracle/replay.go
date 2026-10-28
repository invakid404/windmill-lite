package oracle

import (
	"errors"
	"sort"
	"strconv"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// checkReplay holds a stub run's answered calls against its walk:
//
//   - every call was answered: a call the table had no outcome for
//     (replay_miss) or whose job was not found in time (replay_unresolved)
//     fails the run, since the fixture's table or tree is wrong;
//   - every call was resolved where the walk put its job: a failed attempt
//     k at <site>/r:<k>, the module's last job at <site> itself with k
//     equal to the number of failed attempts before it
//     (replay_site_mismatch);
//   - every walked job that executed a script called the service, so no
//     original body ran (stub_not_called).
func checkReplay(w *Walk, calls []conformance.ReplayCall) error {
	called := map[string]bool{}
	attempts := map[conformance.SitePath]bool{}
	for _, site := range w.IDs {
		if segs := site.Segments(); len(segs) > 0 && segs[len(segs)-1].Kind() == conformance.KindAttempt {
			attempts[site] = true
		}
	}
	for _, c := range calls {
		if c.Err != "" {
			return errors.New(c.Err)
		}
		called[c.Job] = true
		walked, ok := w.IDs[c.Job]
		failed := c.Site.Child(conformance.Attempt(c.Attempt))
		last := walked == c.Site && !attempts[failed] && (c.Attempt == 0 || attempts[c.Site.Child(conformance.Attempt(c.Attempt-1))])
		if !ok || walked != failed && !last {
			got := "(not walked)"
			if ok {
				got = walked.String()
			}
			return refusal("replay_site_mismatch", "job", jsonString(c.Job), "resolved", jsonString(c.Site.String()), "attempt", strconv.Itoa(c.Attempt), "walked", jsonString(got))
		}
	}
	var missing []string
	for id, site := range w.IDs {
		if scriptKinds[w.Timings[site.String()].Kind] && !called[id] {
			missing = append(missing, site.String())
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return refusal("stub_not_called", "sites", jsonStrings(missing))
	}
	return nil
}

func jsonStrings(ss []string) string {
	out := "["
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += jsonString(s)
	}
	return out + "]"
}
