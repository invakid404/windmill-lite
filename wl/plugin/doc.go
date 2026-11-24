// Package plugin is the host's side of windmill-lite's go-plugin adapter:
// it launches the engine as a [hashicorp/go-plugin] plugin, pins its
// executable by SHA-256, and calls its Runner service ([ADR 0009]).
//
// The Runner service and its messages are defined in
// proto/windmill_lite/v1/runner.proto. The Go code generated from that file
// is in this package: runner.pb.go and runner_grpc.pb.go, which
// `go generate ./wl/plugin` writes again.
//
// A host launches the engine in four steps:
//
//  1. ClientConfig turns a Launch into go-plugin's client configuration
//     and a cleanup, which the host calls once the launch has ended.
//  2. go-plugin's NewClient and Client start the engine and connect.
//  3. Dispense(PluginName) returns a *Client, whose BuildInfo the host
//     compares with the release record that it ships.
//  4. The Client's calls run flows and scripts, and Kill stops the engine.
//
// The engine's command `windmill-lite plugin` serves this plugin.
//
// [hashicorp/go-plugin]: https://github.com/hashicorp/go-plugin
// [ADR 0009]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0009-host-adapters.md#the-go-plugin-adapter
package plugin

//go:generate sh ../../proto/generate.sh
