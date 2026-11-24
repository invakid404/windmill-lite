package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

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

// waitServing polls the engine's health until it answers SERVING, or ctx
// ends: the engine serves while it opens its bundle, and until then health
// answers NOT_SERVING, and BuildInfo and Run answer UNAVAILABLE.
func waitServing(ctx context.Context, engine *plugin.Client) error {
	for {
		st, err := engine.Health(ctx)
		if err == nil && st == healthpb.HealthCheckResponse_SERVING {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the engine did not serve (last health %v, %v): %w", st, err, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// launchAndRun is the README's example: it launches the engine, waits until
// it serves, admits it against the release record and runs a flow.
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

	if err := waitServing(ctx, engine); err != nil {
		return err
	}
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

// This example launches the engine of a release, waits until it serves,
// admits it against the release record, and runs a flow. It needs an engine that serves the
// Runner plugin, so it is compiled but not run.
func Example() {
	_ = launchAndRun
}

// superviseAndRun is the README's example of the supervisor: it runs the
// engine of a release under a Supervisor until ctx ends, and runs a flow on
// the generation that serves.
func superviseAndRun(ctx context.Context, want release) error {
	sup, err := plugin.NewSupervisor(plugin.SupervisorConfig{
		Launch: plugin.Launch{
			Path:   "/opt/windmill-lite/windmill-lite",
			SHA256: want.EngineSHA256,
			Config: "/etc/windmill-lite/plugin.json",
			Env:    []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
		},
		Identity: plugin.PinnedIdentity{
			BundleDigest:  want.Bundle,
			EngineVersion: want.Engine,
			BunVersion:    want.Bun,
			ConfigDigest:  want.ConfigDigest,
			Labels:        want.Labels,
		},
		// The configuration's drain_timeout_ms.
		DrainTimeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	err = sup.Call(ctx, func(engine *plugin.Client) error {
		res, err := engine.Run(ctx, &plugin.RunRequest{
			Path: "f/app/main",
			Args: []byte(`{"name": "lite"}`),
		})
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", res.GetStatus(), res.GetResult())
		return nil
	})
	<-done
	return err
}

// This example supervises the engine of a release, runs a flow on the
// generation that serves, and stops the engine when ctx ends. It needs an
// engine that serves the Runner plugin, so it is compiled but not run.
func ExampleSupervisor() {
	_ = superviseAndRun
}
