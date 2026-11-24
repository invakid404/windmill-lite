package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/invakid404/windmill-lite/internal/bundle"
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
	var foreign []string
	fs.Func("foreign-windmill", "the `origin` of a real Windmill, such as https://windmill.example.com: a step's dispatch there loses windmill-lite's job ids; repeat it for each origin", func(v string) error {
		foreign = append(foreign, v)
		return nil
	})
	resultPolls := fs.Bool("result-polls-for-all-jobs", false, "answer the result polls for every job of a live run that the job read answers for, not only for the runs that steps dispatched")
	store := storeFlags(fs)
	quickJSOnly := fs.Bool("quickjs-only", false, "evaluate every expression the shortcuts and member paths do not answer in QuickJS, without the Go evaluator (ADR 0003)")
	checkBundle := fs.String("check-bundle", "", "check the bundle against its manifest, -digest and this `digest`, as Open does, and exit without connecting (the host SDK runs it before it loads a bundle's own plugin)")
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
	if *checkBundle != "" {
		return checkHostBundle(*bundlePath, *digest, *checkBundle, fail)
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
	opts := wl.Options{DataDir: *data, Digest: *digest, EngineVersion: resolveVersion(), Diagnostics: stderr, Host: host, PluginTimeout: *pluginTimeout, ReadCache: *readCache, QuickJSOnly: *quickJSOnly, ForeignWindmillOrigins: foreign, ResultPollsForAllJobs: *resultPolls}
	if opts.Variables, err = hostVariables(*variables); err == nil {
		opts.Resources, err = hostResources(*resources)
	}
	var closeStore func()
	if err == nil {
		opts.ResourceStore, closeStore, err = store.open(stderr)
	}
	if closeStore != nil {
		// Deferred first, so it runs after the runtime's Close.
		defer closeStore()
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

// checkHostBundle is `host -check-bundle`: the checks of the bundle that
// wl.Open makes in host mode before the handshake, with the flags as this
// command reads them. The bundle must be a directory, and verify against
// its manifest and the -digest pin, if any; its digest must be want, the
// digest of the manifest that the host read; and this engine version must
// have built it. The host SDK runs it before it loads a bundle's own
// plugin, which runs before the handshake could report a refusal.
func checkHostBundle(dir, pin, want string, fail func(error) int) int {
	// Host mode opens a bundle directory, which the host loads too, and
	// never an archive.
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("host mode opens a bundle directory, and %s is not one", dir))
	}
	v, err := bundle.Verify(dir, bundle.VerifyOptions{Digest: pin})
	if err != nil {
		return fail(err)
	}
	if v.Digest != want {
		return fail(fmt.Errorf("the bundle's digest is %s, not %s, the digest of the manifest that the host read", v.Digest, want))
	}
	if engine := resolveVersion(); v.Manifest.Engine.Version != engine {
		return fail(fmt.Errorf("engine %s built the bundle; this engine is %s", v.Manifest.Engine.Version, engine))
	}
	return 0
}
