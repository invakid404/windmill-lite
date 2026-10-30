package oracle

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultCLICommand runs the pinned CLI from npm on the harness host.
var DefaultCLICommand = []string{"npx", "-y", CLIPackage}

// CLI runs the published windmill-cli as a black-box tool on the harness
// host, never the CLI inside the image (OR-2, OR-8). It is not a
// dependency of windmill-lite: no manifest names it and no code of it is
// read or copied (Q-OF-7(a), decided no); running a published tool is use
// (licensing.md, LIC-1).
type CLI struct {
	// Command runs the CLI, for example DefaultCLICommand.
	Command []string
	// Home is the HOME the CLI sees: a session directory, so no user
	// profile, npm configuration or package cache leaks in (OR-8, OR-18).
	Home string
	// ConfigDir is the CLI's own configuration directory (OR-8).
	ConfigDir string
	// Stdout and Stderr receive the CLI's output; nil discards it.
	Stdout, Stderr io.Writer
}

// Target is the oracle workspace a CLI command acts on.
type Target struct {
	BaseURL, Token, Workspace string
}

// env is the CLI's whole environment: PATH and the session's own
// directories. Nothing else is inherited, so no WM_* variable or user
// setting reaches it.
func (c CLI) env() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + c.Home,
		"npm_config_cache=" + filepath.Join(c.Home, "npm-cache"),
		"npm_config_update_notifier=false",
		"WMILL_CONFIG_DIR=" + c.ConfigDir,
		"NO_COLOR=1",
	}
}

// args returns the command line of a CLI command on t: the global flags,
// then the command (OR-8).
func (c CLI) args(t Target, command ...string) []string {
	a := append([]string(nil), c.Command[1:]...)
	a = append(a, "--base-url", t.BaseURL, "--token", t.Token, "--workspace", t.Workspace, "--config-dir", c.ConfigDir)
	return append(a, command...)
}

// run runs a CLI command on t in dir. The CLI holds t's token, so the
// token is replaced by RedactedToken in its output and in the error.
func (c CLI) run(ctx context.Context, dir string, t Target, command ...string) error {
	if len(c.Command) == 0 {
		return fmt.Errorf("oracle: no CLI command")
	}
	args := c.args(t, command...)
	cmd := exec.CommandContext(ctx, c.Command[0], args...)
	ownProcessGroup(cmd)
	cmd.Dir = dir
	cmd.Env = c.env()
	var stdout *redactingWriter
	if c.Stdout != nil {
		stdout = &redactingWriter{w: c.Stdout, token: t.Token}
		cmd.Stdout = stdout
	}
	var stderr bytes.Buffer
	errOut := &redactingWriter{w: orDiscard(c.Stderr), token: t.Token}
	cmd.Stderr = io.MultiWriter(&stderr, errOut)
	err := cmd.Run()
	if stdout != nil {
		_ = stdout.Flush()
	}
	_ = errOut.Flush()
	if err != nil {
		return fmt.Errorf("oracle: %s %s: %w: %s", c.Command[0], strings.Join(redact(args), " "), err, redactTokenString(strings.TrimSpace(stderr.String()), t.Token))
	}
	return nil
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// redact hides the token in an error message.
func redact(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--token" {
			out[i+1] = "<token>"
		}
	}
	return out
}

// Version returns the CLI's --version output and fails unless it names
// CLIVersion: a different CLI is a provenance failure, never a silent
// change of loader (OR-8).
func (c CLI) Version(ctx context.Context) (string, error) {
	if len(c.Command) == 0 {
		return "", fmt.Errorf("oracle: no CLI command")
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Command[0], append(append([]string(nil), c.Command[1:]...), "--version")...)
	ownProcessGroup(cmd)
	cmd.Env = c.env()
	cmd.Dir = c.Home
	cmd.Stdout = &out
	cmd.Stderr = orDiscard(c.Stderr)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("oracle: %s --version: %w", strings.Join(c.Command, " "), err)
	}
	v := strings.TrimSpace(out.String())
	if got := reportedVersion(v); got != CLIVersion {
		return v, refusal("cli_version_mismatch", "want", jsonString(CLIVersion), "got", jsonString(got))
	}
	return v, nil
}

// versionToken is a version as a CLI prints it: three numbers and any
// pre-release or build suffix.
var versionToken = regexp.MustCompile(`(?:^|[^0-9A-Za-z.])v?([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*)`)

// reportedVersion returns the version the --version output reports: the
// first version on its first line that has one. The CLI prints "CLI
// version: 1.767.0" and may add an "outdated, latest is …" notice on a
// later line, which is not its version. It returns "" when there is none.
func reportedVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if m := versionToken.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// AddMissingFolders runs `folder add-missing --yes` in a sync tree. It
// writes folder.meta.yaml for folders that lack it (OR-8).
func (c CLI) AddMissingFolders(ctx context.Context, tree string, t Target) error {
	return c.run(ctx, tree, t, "folder", "add-missing", "--yes")
}

// GenerateMetadata runs `generate-metadata --yes` in a sync tree against
// the oracle, writing script metadata, locks and wmill-lock.yaml (OR-8).
func (c CLI) GenerateMetadata(ctx context.Context, tree string, t Target) error {
	return c.run(ctx, tree, t, "generate-metadata", "--yes")
}

// Push runs `sync push --yes` in a sync tree. Push is destructive: it
// removes selected remote items absent from the tree, so the harness only
// pushes into a workspace it created for the run (OR-31).
func (c CLI) Push(ctx context.Context, tree string, t Target) error {
	return c.run(ctx, tree, t, "sync", "push", "--yes")
}

// Pull runs `sync pull --yes` in dir, which holds only a wmill.yaml: the
// CLI writes the workspace's items there as a sync tree, the round trip of
// SY-6 and CF-6 (oracle.pull).
func (c CLI) Pull(ctx context.Context, dir string, t Target) error {
	return c.run(ctx, dir, t, "sync", "pull", "--yes")
}

// CopyTree copies the regular files and directories below src into dst,
// which must not exist. The harness pushes from a copy, so the push cannot
// change the checkout; it copies a sync tree with CopyLayout, which also
// carries what the tree's codebase entries name outside it.
func CopyTree(src, dst string) error {
	return copyPath(src, dst)
}

// copyPath copies src, a regular file or a directory with the regular
// files and directories below it, to dst, making dst's parent directories.
func copyPath(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		default:
			return fmt.Errorf("oracle: %s is not a regular file or directory", p)
		}
	})
}
