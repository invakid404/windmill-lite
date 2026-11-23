package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"maps"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// release is the record of the engine release that a host ships: what it
// pins of the engine's build identity.
type release struct {
	EngineSHA256 string
	Bundle       string
	Engine       string
	Bun          string
	ConfigDigest string
	Labels       map[string]string
}

// admit refuses an engine whose build identity differs from the release
// record on any field that the host pins.
func admit(info *plugin.BuildInfo, want release) error {
	switch {
	case info.GetBundleDigest() != want.Bundle:
		return fmt.Errorf("the engine serves the bundle %s, want %s", info.GetBundleDigest(), want.Bundle)
	case info.GetEngineVersion() != want.Engine:
		return fmt.Errorf("the engine is %s, want %s", info.GetEngineVersion(), want.Engine)
	case info.GetBunVersion() != want.Bun:
		return fmt.Errorf("the engine runs Bun %s, want %s", info.GetBunVersion(), want.Bun)
	case info.GetConfigDigest() != want.ConfigDigest:
		return fmt.Errorf("the engine has the configuration %s, want %s", info.GetConfigDigest(), want.ConfigDigest)
	case !maps.Equal(info.GetLabels(), want.Labels):
		return fmt.Errorf("the engine has the labels %v, want %v", info.GetLabels(), want.Labels)
	}
	return nil
}

// launchAndRun is the README's example: it launches the engine, admits it
// against the release record and runs a flow.
func launchAndRun(ctx context.Context, want release) error {
	cfg, cleanup, err := plugin.ClientConfig(plugin.Launch{
		Path:   "/opt/windmill-lite/windmill-lite",
		SHA256: want.EngineSHA256,
		Config: "/etc/windmill-lite/plugin.json",
		Env:    []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
	})
	if err != nil {
		return err
	}
	defer cleanup()
	client := goplugin.NewClient(cfg)
	defer client.Kill()

	conn, err := client.Client()
	if err != nil {
		return err
	}
	raw, err := conn.Dispense(plugin.PluginName)
	if err != nil {
		return err
	}
	engine := raw.(*plugin.Client)

	info, err := engine.BuildInfo(ctx)
	if err != nil {
		return err
	}
	if err := admit(info, want); err != nil {
		return err
	}

	res, err := engine.Run(ctx, &plugin.RunRequest{
		Path: "f/app/main",
		Args: []byte(`{"name": "lite"}`),
	})
	switch {
	case errors.Is(err, plugin.ErrNotStarted):
		return fmt.Errorf("the run did not start, so it can be retried: %w", err)
	case errors.Is(err, plugin.ErrOutcomeUnknown):
		return fmt.Errorf("the run may have mutated: %w", err)
	case err != nil:
		return err
	}
	fmt.Printf("%s: %s\n", res.GetStatus(), res.GetResult())
	return nil
}

// This example launches the engine of a release, admits it against the
// release record, and runs a flow. It needs an engine that serves the
// Runner plugin, so it is compiled but not run.
func Example() {
	_ = launchAndRun
}
