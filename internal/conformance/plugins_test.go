package conformance

import "testing"

// The plugin case file loads: its cases decode, with unique ids, and its
// folder and contract can be written.
func TestPluginFileLoads(t *testing.T) {
	f, err := LoadPluginFile("../../conformance/plugins/hooks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 10 || f.TimeoutMS <= 0 || len(f.Scripts) == 0 {
		t.Fatalf("%d cases, timeout %d, %d scripts", len(f.Cases), f.TimeoutMS, len(f.Scripts))
	}
	if err := f.WriteFolder(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(f.Contract()) == 0 {
		t.Fatal("no contract")
	}
}
