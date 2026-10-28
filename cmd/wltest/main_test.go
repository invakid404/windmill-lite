package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 2, "usage: wltest record"},
		{[]string{"frobnicate"}, 2, "usage: wltest record"},
		{[]string{"help"}, 0, "usage: wltest record"},
		{[]string{"check", "conformance/flows"}, 2, "wltest check is not implemented yet"},
		{[]string{"stub", "x"}, 2, "wltest stub is not implemented yet"},
		{[]string{"record"}, 2, "usage: wltest record"},
		{[]string{"record", "--workers", "3", "x"}, 2, "usage: wltest record"},
		{[]string{"record", "--no-such-flag", "x"}, 2, "flag provided but not defined"},
		{[]string{"record", t.TempDir()}, 1, "no fixtures"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), c.args, &stdout, &stderr)
		if code != c.code || !strings.Contains(stdout.String()+stderr.String(), c.out) {
			t.Errorf("%q: exit %d, output %q; want exit %d with %q", c.args, code, stdout.String()+stderr.String(), c.code, c.out)
		}
	}
}

func TestHarnessInfo(t *testing.T) {
	h := harnessInfo()
	if h.Name != "wltest" || !strings.HasPrefix(h.Go, "go") {
		t.Fatalf("%+v", h)
	}
}
