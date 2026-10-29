package oracle

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestDockerAllowlist: the harness can pull, inspect and run compose, and
// cannot tag, push, build or otherwise derive an image (OR-3).
func TestDockerAllowlist(t *testing.T) {
	for _, ok := range [][]string{
		{"info", "--format", "{{.DockerRootDir}}"},
		{"pull", WindmillImage},
		{"image", "inspect", "--format", "{{json .}}", WindmillImage},
		{"image", "rm", WindmillImage},
		{"ps", "-a", "-q"},
		{"volume", "ls", "-q"},
		{"network", "ls", "-q"},
		{"compose", "-p", ProjectName, "-f", "c.yaml", "--profile", "workers2", "up", "-d"},
		{"compose", "-p", ProjectName, "-f", "c.yaml", "down", "-v"},
	} {
		if err := CheckDockerArgs(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range [][]string{
		{"push", WindmillImage},
		{"tag", WindmillImage, "mine/windmill"},
		{"image", "push", WindmillImage},
		{"image", "tag", WindmillImage, "mine/windmill"},
		{"image", "save", WindmillImage},
		{"build", "."},
		{"buildx", "imagetools", "create", WindmillImage},
		{"commit", "abc"},
		{"save", WindmillImage},
		{"export", "abc"},
		{"run", WindmillImage},
		{"exec", "abc", "sh"},
		{"compose", "-p", ProjectName, "push"},
		{"compose", "-p", ProjectName, "build"},
		{"compose", "-p", ProjectName, "-f", "c.yaml", "exec", "windmill", "sh"},
		{},
	} {
		if err := CheckDockerArgs(bad); err == nil {
			t.Errorf("%q allowed", bad)
		}
	}
}

// TestComposeFile checks the compose file against the pins and the
// hygiene rules: images by digest, never pulled by compose, ports on
// 127.0.0.1 only, Postgres on tmpfs, no volumes, and the recorded oracle
// environment (OR-1, OR-4, OR-7, OR-18).
func TestComposeFile(t *testing.T) {
	var c struct {
		Name     string `yaml:"name"`
		Services map[string]struct {
			Image       string            `yaml:"image"`
			PullPolicy  string            `yaml:"pull_policy"`
			Environment map[string]string `yaml:"environment"`
			Ports       []string          `yaml:"ports"`
			Tmpfs       []string          `yaml:"tmpfs"`
			Volumes     []string          `yaml:"volumes"`
			MemLimit    string            `yaml:"mem_limit"`
			Profiles    []string          `yaml:"profiles"`
			ExtraHosts  []string          `yaml:"extra_hosts"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(ComposeFile(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Name != ProjectName || c.Volumes != nil {
		t.Fatalf("project %q, volumes %v", c.Name, c.Volumes)
	}
	want := map[string]struct{ image, mem string }{
		"db":       {PostgresImage, "1g"},
		"windmill": {WindmillImage, "2g"},
		"worker":   {WindmillImage, "2g"},
	}
	if len(c.Services) != len(want) {
		t.Fatalf("services %v", c.Services)
	}
	for name, w := range want {
		s := c.Services[name]
		if s.Image != w.image || s.MemLimit != w.mem || s.PullPolicy != "never" || len(s.Volumes) != 0 {
			t.Errorf("service %s: %+v", name, s)
		}
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("service %s publishes %s beyond 127.0.0.1", name, p)
			}
		}
	}
	if !reflect.DeepEqual(c.Services["db"].Tmpfs, []string{"/var/lib/postgresql/data"}) {
		t.Errorf("db tmpfs %v", c.Services["db"].Tmpfs)
	}
	if !reflect.DeepEqual(c.Services["worker"].Profiles, []string{"workers2"}) || c.Services["worker"].Environment["MODE"] != "worker" {
		t.Errorf("worker %+v", c.Services["worker"])
	}
	env := c.Services["windmill"].Environment
	for k, v := range OracleEnv {
		// The knob of OR-7/a defaults to OracleEnv's value, and a session
		// may set it (Session.ListenInterval).
		if k == "LISTEN_NEW_EVENTS_INTERVAL_SEC" {
			v = "${" + ListenIntervalVar + ":-" + v + "}"
		}
		if env[k] != v {
			t.Errorf("windmill %s = %q, OracleEnv says %q", k, env[k], v)
		}
	}
	for k := range env {
		if _, ok := OracleEnv[k]; !ok && k != "DATABASE_URL" && k != "WL_EFFECT_SINK" {
			t.Errorf("windmill sets %s, which OracleEnv does not record", k)
		}
	}
	if !strings.Contains(env["WL_EFFECT_SINK"], "WL_EFFECT_SINK:?") {
		t.Errorf("WL_EFFECT_SINK must be required from wltest: %q", env["WL_EFFECT_SINK"])
	}
	if !reflect.DeepEqual(Compose{File: "c.yaml", Workers: 2}.args("up"), []string{"compose", "-p", ProjectName, "-f", "c.yaml", "--profile", "workers2", "up"}) {
		t.Error("two workers enable the workers2 profile")
	}
}

// TestImageIdentity is OR-1 and OR-V5/b: the pinned digest must be among
// the image's RepoDigests, else the preflight fails on its own.
func TestImageIdentity(t *testing.T) {
	wm, pg := Images[1], Images[0]
	if err := wm.CheckIdentity(ImageInfo{RepoDigests: []string{WindmillRepo + "@" + WindmillDigest}}); err != nil {
		t.Error(err)
	}
	for _, d := range []string{"postgres@" + PostgresDigest, "docker.io/library/postgres@" + PostgresDigest} {
		if err := pg.CheckIdentity(ImageInfo{RepoDigests: []string{d}}); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	// spec: OR-V5/b
	err := wm.CheckIdentity(ImageInfo{RepoDigests: []string{WindmillRepo + "@sha256:0e743cd6"}})
	var r *Refusal
	if !errors.As(err, &r) || r.Code != "oracle_image_mismatch" {
		t.Fatalf("a different digest: %v", err)
	}
	if err := wm.CheckIdentity(ImageInfo{RepoDigests: []string{"ghcr.io/someone/windmill@" + WindmillDigest}}); err == nil {
		t.Error("the pinned digest under another repository passed")
	}
}

// TestDockerEnvDropsTopologyVariables: variables that would change the
// compose topology never reach docker (OR-4, OR-5).
func TestDockerEnvDropsTopologyVariables(t *testing.T) {
	got := dockerEnv([]string{"PATH=/bin", "COMPOSE_PROFILES=workers2", "COMPOSE_FILE=x.yml", "DOCKER_DEFAULT_PLATFORM=linux/arm64", "DOCKER_HOST=unix:///x", "HOME=/h"})
	if want := []string{"PATH=/bin", "DOCKER_HOST=unix:///x", "HOME=/h"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
