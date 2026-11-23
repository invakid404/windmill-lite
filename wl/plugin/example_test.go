package plugin_test

import (
	"context"
	"errors"
	"fmt"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// launchAndRun is the README's example: it launches the engine, checks its
// build identity and runs a flow.
func launchAndRun(ctx context.Context, engineSHA256, wantBundle string) error {
	cfg, err := plugin.ClientConfig(plugin.Launch{
		Path:   "/opt/windmill-lite/windmill-lite",
		SHA256: engineSHA256,
		Config: "/etc/windmill-lite/plugin.json",
		Env:    []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
	})
	if err != nil {
		return err
	}
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
	if info.GetBundleDigest() != wantBundle {
		return fmt.Errorf("the engine serves the bundle %s, want %s", info.GetBundleDigest(), wantBundle)
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

// This example launches the engine from its release, checks the bundle
// that it serves, and runs a flow. It needs an engine that serves the
// Runner plugin, so it is compiled but not run.
func Example() {
	_ = launchAndRun
}
