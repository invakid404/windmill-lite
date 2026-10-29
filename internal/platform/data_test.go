package platform

import (
	"context"
	"os"
	"testing"
)

// TestLoadData reads a bundle's data/ in the format internal/bundle writes
// (ADR 0010): its resources and variables are the local default's.
func TestLoadData(t *testing.T) {
	s, err := LoadData(os.DirFS("testdata/bundle"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Services{Resources: s, Variables: s}
	b, err := svc.InterpolatedResource(context.Background(), "f/app/db", nil)
	if err != nil || string(b) != `{"host":"h","password":"p"}` {
		t.Fatalf("%s %v", b, err)
	}
	if r, ok, _ := s.Resource(context.Background(), "f/app/db"); !ok || r.Type != "postgresql" {
		t.Fatalf("%+v %v", r, ok)
	}
	// A bundle without data/ has nothing.
	empty, err := LoadData(os.DirFS("testdata"))
	if err != nil {
		t.Fatal(err)
	}
	if rs, vs := empty.Paths(); len(rs) != 0 || len(vs) != 0 {
		t.Fatalf("%v %v", rs, vs)
	}
}
