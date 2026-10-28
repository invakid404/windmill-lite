package oracle

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed compose.yaml
var composeFile []byte

// ComposeFile returns the oracle's compose file.
func ComposeFile() []byte { return append([]byte(nil), composeFile...) }

// OracleEnv is the oracle's non-default environment, recorded in every
// recording's provenance (OR-7). It must match the windmill service of
// compose.yaml; a test checks that. WL_EFFECT_SINK is added per session.
var OracleEnv = map[string]string{
	"MODE":                           "standalone",
	"BASE_URL":                       BaseURL,
	"LISTEN_NEW_EVENTS_INTERVAL_SEC": "1",
	"WHITELIST_ENVS":                 "WL_EFFECT_SINK",
}

// Docker runs the docker CLI.
type Docker interface {
	// Run runs docker with args and extra environment variables
	// (KEY=VALUE) and returns its standard output.
	Run(ctx context.Context, env []string, args ...string) ([]byte, error)
}

// ExecDocker runs the docker binary on PATH. Every call is checked by
// CheckDockerArgs first.
type ExecDocker struct {
	// Stderr receives docker's standard error; nil discards it into the
	// returned error message.
	Stderr *os.File
}

// Run implements Docker.
func (d ExecDocker) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	if err := CheckDockerArgs(args); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	ownProcessGroup(cmd) // docker compose runs as a child plugin
	cmd.Env = append(dockerEnv(os.Environ()), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if d.Stderr != nil {
		_, _ = d.Stderr.Write(stderr.Bytes())
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// dockerEnv drops the variables that would change the oracle's topology
// behind the compose file's back: compose profiles, files, project names
// and env files, and a default platform (OR-4, OR-5).
func dockerEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, e := range environ {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "COMPOSE_") || k == "DOCKER_DEFAULT_PLATFORM" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// dockerAllowed lists the docker subcommands the harness may run, by
// command and, for groups, subcommand. push, tag, build, commit, save,
// export and every other command that could derive, re-tag or re-push an
// image are absent, so the harness cannot do any of that (OR-3).
var dockerAllowed = map[string]map[string]bool{
	"info":    nil,
	"version": nil,
	"pull":    nil,
	"inspect": nil,
	"ps":      nil,
	"image":   {"inspect": true, "rm": true},
	"volume":  {"ls": true},
	"network": {"ls": true},
	"compose": {"up": true, "down": true, "ps": true, "logs": true},
}

// CheckDockerArgs refuses a docker invocation outside dockerAllowed.
func CheckDockerArgs(args []string) error {
	cmd, rest := firstWord(args)
	sub, ok := dockerAllowed[cmd]
	if !ok {
		return fmt.Errorf("oracle: docker %q is not a command the harness runs", cmd)
	}
	if sub == nil {
		return nil
	}
	if cmd == "compose" {
		rest = skipComposeFlags(rest)
	}
	s, _ := firstWord(rest)
	if !sub[s] {
		return fmt.Errorf("oracle: docker %s %q is not a command the harness runs", cmd, s)
	}
	return nil
}

func firstWord(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

// skipComposeFlags drops the global compose flags the harness passes
// before the subcommand: -p/--project-name, -f/--file and --profile, each
// with its value.
func skipComposeFlags(args []string) []string {
	for len(args) >= 2 {
		switch args[0] {
		case "-p", "--project-name", "-f", "--file", "--profile":
			args = args[2:]
		default:
			return args
		}
	}
	return args
}

// Pinned is an image of an oracle session, referenced by tag and digest.
type Pinned struct {
	// Ref is the reference compose uses, tag and digest.
	Ref string
	// Repo and Digest identify the image (OR-1).
	Repo, Digest string
	// Service is the compose service that runs it.
	Service string
}

// Images are the images of an oracle session: the database and Windmill.
var Images = []Pinned{
	{Ref: PostgresImage, Repo: PostgresRepo, Digest: PostgresDigest, Service: "db"},
	{Ref: WindmillImage, Repo: WindmillRepo, Digest: WindmillDigest, Service: "windmill"},
}

// CheckIdentity requires the image's RepoDigests to carry the pinned
// digest (OR-1). A mismatch fails the preflight on its own, whatever a
// comparison would say (OR-V5/b). Docker Hub images may be listed with
// their docker.io/library/ prefix.
func (p Pinned) CheckIdentity(info ImageInfo) error {
	for _, d := range info.RepoDigests {
		repo, digest, _ := strings.Cut(d, "@")
		repo = strings.TrimPrefix(strings.TrimPrefix(repo, "docker.io/"), "library/")
		if repo == p.Repo && digest == p.Digest {
			return nil
		}
	}
	return ErrImageMismatch(p.Ref, info.RepoDigests)
}

// ImageInfo is what docker image inspect reports about a pulled image.
type ImageInfo struct {
	ID          string            `json:"Id"`
	RepoDigests []string          `json:"RepoDigests"`
	Size        int64             `json:"Size"`
	Created     string            `json:"Created"`
	Labels      map[string]string `json:"-"`
	Arch        string            `json:"Architecture"`
}

// InspectImage reports the image, or ok false when it is not present.
func InspectImage(ctx context.Context, d Docker, ref string) (info ImageInfo, ok bool, err error) {
	out, err := d.Run(ctx, nil, "image", "inspect", "--format", "{{json .}}", ref)
	if err != nil {
		if strings.Contains(err.Error(), "No such image") || strings.Contains(err.Error(), "No such object") {
			return ImageInfo{}, false, nil
		}
		return ImageInfo{}, false, err
	}
	var raw struct {
		ImageInfo
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return ImageInfo{}, false, fmt.Errorf("oracle: docker image inspect %s: %w", ref, err)
	}
	info = raw.ImageInfo
	info.Labels = raw.Config.Labels
	return info, true, nil
}

// DockerDataRoot returns the directory whose filesystem the disk policy
// measures (OR-30 step 1): docker's data root when it exists on this host,
// else the user's home directory, where Docker Desktop keeps its disk
// image.
func DockerDataRoot(ctx context.Context, d Docker) (string, error) {
	out, err := d.Run(ctx, nil, "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		return root, nil
	}
	return os.UserHomeDir()
}

// DockerArch returns the docker server's architecture as amd64 or arm64.
func DockerArch(ctx context.Context, d Docker) (string, error) {
	out, err := d.Run(ctx, nil, "info", "--format", "{{.Architecture}}")
	if err != nil {
		return "", err
	}
	switch a := strings.TrimSpace(string(out)); a {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return a, nil
	}
}

// Compose drives the oracle's compose project.
type Compose struct {
	Docker Docker
	// File is the compose file on disk.
	File string
	// Workers is 1 or 2; 2 enables the workers2 profile (OR-5).
	Workers int
	// Env holds the variables compose interpolates (WL_EFFECT_SINK).
	Env []string
}

// WriteComposeFile writes the compose file into dir and returns its path.
func WriteComposeFile(dir string) (string, error) {
	p := filepath.Join(dir, "compose.yaml")
	return p, os.WriteFile(p, composeFile, 0o644)
}

func (c Compose) args(sub ...string) []string {
	a := []string{"compose", "-p", ProjectName, "-f", c.File}
	if c.Workers == 2 {
		a = append(a, "--profile", "workers2")
	}
	return append(a, sub...)
}

// Up starts the project.
func (c Compose) Up(ctx context.Context) error {
	_, err := c.Docker.Run(ctx, c.Env, c.args("up", "-d")...)
	return err
}

// Down removes the project's containers, network and volumes (OR-18).
func (c Compose) Down(ctx context.Context) error {
	_, err := c.Docker.Run(ctx, c.Env, c.args("down", "-v", "--remove-orphans")...)
	return err
}

// ContainerID returns the id of the running container of a service.
func (c Compose) ContainerID(ctx context.Context, service string) (string, error) {
	out, err := c.Docker.Run(ctx, c.Env, c.args("ps", "-q", service)...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("oracle: service %s has no container", service)
	}
	return id, nil
}

// Leftovers lists the containers, volumes and networks that carry the
// session's compose project label. After a teardown all three must be
// empty (OR-18/a); before a session starts, anything left over means an
// earlier session was not torn down.
func Leftovers(ctx context.Context, d Docker) ([]string, error) {
	filter := "label=com.docker.compose.project=" + ProjectName
	var left []string
	for _, q := range [][]string{
		{"ps", "-a", "-q", "--filter", filter},
		{"volume", "ls", "-q", "--filter", filter},
		{"network", "ls", "-q", "--filter", filter},
	} {
		out, err := d.Run(ctx, nil, q...)
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Fields(string(out)) {
			left = append(left, q[0]+" "+l)
		}
	}
	return left, nil
}

// MemoryPeak reads a container's cgroup v2 memory.peak from the host, for
// provenance (OR-4, Q-OR-2). It reports ok false where the host does not
// expose it (Docker Desktop, cgroup v1, other drivers).
func MemoryPeak(containerID string) (peak int64, ok bool) {
	for _, p := range []string{
		"/sys/fs/cgroup/system.slice/docker-" + containerID + ".scope/memory.peak",
		"/sys/fs/cgroup/docker/" + containerID + "/memory.peak",
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}
