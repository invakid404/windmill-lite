package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/wl"
)

// runHost is `windmill-lite host`: the engine side of host mode (ADR 0004
// host mode, ADR 0009), started by a host such as the Bun host SDK with
// its socket as file descriptor 3. It opens the bundle with wl in host
// mode (wl.Options.Host), which completes the handshake with the host,
// and serves the host's runs, whose steps run in the host, until the
// host's connection ends. SIGTERM and SIGINT end it: the runs in flight
// are canceled and the connection closed.
func runHost(args []string, stderr io.Writer) int {
	fs := newFlagSet("host", stderr)
	bundlePath := fs.String("bundle", "", "the bundle `directory`, which the host loads too")
	workdir := fs.String("workdir", "", "the working `directory` the host passed, which must be the host's own and writable")
	killAfter := fs.Duration("kill-after", harness.DefaultKillAfter, "the watchdog's threshold of silence from the host; 0 disables it")
	data := fs.String("data", "", "the data `directory` the runtime writes into (default: a temporary one, removed afterwards)")
	digest := fs.String("digest", "", "the bundle `digest` the bundle must have")
	variables := fs.String("variables", "", "a JSON `file` of host variables, keyed by path, as in a bundle's data/variables.json; secrets go here")
	resources := fs.String("resources", "", "a JSON `file` of host resources, keyed by path, as in a bundle's data/resources.json")
	pluginTimeout := fs.Duration("plugin-timeout", 0, "how long a host_call to the host's plugin waits for its reply (default 1m)")
	cpuprofile := fs.String("cpuprofile", "", "write a CPU profile of the engine to `file`, for go tool pprof, until the host's connection ends")
	readCache := fs.Bool("read-cache", false, "read each resource and variable once per run (deviation D21 of ADR 0001)")
	skipArgsFile := fs.Bool("skip-args-file", false, "the host's own code never reads args.json: write none for a bundle none of whose scripts can read it")
	pos, ok := parse(fs, args)
	if !ok {
		return 2
	}
	if *bundlePath == "" || *workdir == "" || len(pos) != 0 || *killAfter < 0 {
		fmt.Fprintln(stderr, "usage: windmill-lite host --bundle <dir> --workdir <dir> [--kill-after <duration>] [-cpuprofile <file>] [flags]")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "windmill-lite host: %v\n", err)
		return 1
	}
	// The host passes the side channel of its steps' child groups as
	// file descriptor 4 (WR-55); a host that passes none announces them on
	// its socket. It is taken first: the socket's duplicate could take
	// that number otherwise.
	children := harness.HostChildren()
	conn, err := harness.HostConn()
	if err != nil {
		return fail(err)
	}
	host := &wl.Host{Conn: conn, Workdir: *workdir, KillAfter: *killAfter, SkipArgsFile: *skipArgsFile}
	if *killAfter == 0 {
		host.KillAfter = -1
	}
	if children != nil {
		defer children.Close()
		host.Children = children
	}
	opts := wl.Options{DataDir: *data, Digest: *digest, EngineVersion: resolveVersion(), Diagnostics: stderr, Host: host, PluginTimeout: *pluginTimeout, ReadCache: *readCache}
	if opts.Variables, err = hostVariables(*variables); err == nil {
		opts.Resources, err = hostResources(*resources)
	}
	stopProfile := func() {}
	if err == nil {
		stopProfile, err = startCPUProfile(*cpuprofile, stderr)
	}
	if err != nil {
		// The host waits for its welcome: it is told why there is none.
		host.Refuse(resolveVersion(), stderr, err)
		return fail(err)
	}
	// The profile is written once the host's connection has ended.
	defer stopProfile()
	// Open refuses the host itself when it fails.
	rt, err := wl.Open(*bundlePath, opts)
	if err != nil {
		return fail(err)
	}
	defer rt.Close()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	select {
	case <-rt.Done():
	case <-sig:
	}
	return 0
}
