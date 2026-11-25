package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/host"
	"github.com/invakid404/windmill-lite/internal/wlext"
	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/plugin"
	"github.com/invakid404/windmill-lite/wl/redisstore"
)

const pluginUsage = "usage: windmill-lite plugin -config <file>"

// runPlugin is `windmill-lite plugin -config <file>` (ADR 0009, the
// go-plugin adapter): the engine as a go-plugin plugin, which a Go host
// launches with wl/plugin. It reads and checks its configuration, completes
// go-plugin's handshake, and then serves while it opens the bundle with wl,
// with the pool's warm start when the configuration asks for it: health
// answers NOT_SERVING and the Runner calls UNAVAILABLE until the open
// completes, then SERVING, and windmill_lite.v1.Runner serves until the
// host stops it or the engine drains, which ends with exit status 0. A
// start that fails, before the handshake or after it, writes its reason to
// standard error, which go-plugin passes to the host's logger, and exits
// with status 1.
func runPlugin(args []string, stderr io.Writer) int {
	fs := newFlagSet("plugin", stderr)
	configPath := fs.String("config", "", "the configuration `file`, JSON, which the host writes")
	pos, ok := parse(fs, args)
	if !ok {
		return 2
	}
	if *configPath == "" || len(pos) != 0 {
		fmt.Fprintln(stderr, pluginUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "windmill-lite plugin: %v\n", err)
		return 1
	}
	// Checked first: go-plugin's own check comes only once the
	// configuration and the bundle have been read.
	if os.Getenv(plugin.Handshake.MagicCookieKey) != plugin.Handshake.MagicCookieValue {
		return fail(errors.New("the engine was not started as a go-plugin plugin: a host launches it with wl/plugin"))
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		return fail(fmt.Errorf("the configuration: %w", err))
	}
	cfg, err := decodePluginConfig(data)
	if err != nil {
		return fail(fmt.Errorf("the configuration %s: %w", *configPath, err))
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	verified, err := cfg.checkBundle()
	if err != nil {
		return fail(err)
	}
	if afterBundleCheck != nil {
		afterBundleCheck(cfg.Bundle)
	}
	opts, closeStore, err := cfg.options(stderr)
	if err != nil {
		return fail(err)
	}
	// The runtime opens only the bundle that the check verified: one that
	// another replaced since does not open.
	opts.Digest = verified
	// Deferred first, so it runs after the runtime's Close.
	defer closeStore()
	// The engine's socket, and the files it creates without a mode of their
	// own, get no group or other permission. Each harness process sets the
	// umask that the engine was started with, so steps create files as they
	// do under wl.
	launch, err := privateUmask()
	if err != nil {
		return fail(err)
	}
	harness.SetLaunchUmask(launch)
	if cfg.recycleRSS() > 0 {
		if _, err := currentRSS(); err != nil {
			return fail(fmt.Errorf("engine_recycle_rss: %w", err))
		}
	}
	// The engine serves in the state Starting while the runtime opens. The
	// open starts once go-plugin has written its handshake line, so an
	// open that fails cannot end the engine before the host has its
	// address, and the engine serves only on a server that exists. A line
	// that does not reach standard output ends the engine: no host can
	// reach it.
	handshake, err := watchHandshake()
	if err != nil {
		return fail(err)
	}
	gate := host.NewGate()
	limit := cfg.maxMessageBytes()
	srv := &servedRunner{RunnerPlugin: plugin.RunnerPlugin{Runner: gate.Runner(), Hooks: gate.Hooks()}}
	var grpcServer atomic.Pointer[grpc.Server]
	// A drain (ADR 0009, health, readiness and drain) starts on SIGTERM,
	// on the engine's memory trigger, or when the warm pool fails to start
	// a process once the engine serves. The first reason starts it. SIGTERM
	// is caught before go-plugin writes the handshake line, so a host that
	// has the engine's address can only drain it.
	// The first reason is reported and makes the Gate drain at once, under
	// its lock, so a Serve that comes after it never publishes SERVING.
	drains := make(chan string, 1)
	var drainOnce sync.Once
	drain := func(why string) {
		drainOnce.Do(func() {
			gate.Drain()
			fmt.Fprintf(stderr, "windmill-lite plugin: draining: %s\n", why)
			drains <- why
		})
	}
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	defer signal.Stop(sigterm)
	go func() {
		for range sigterm {
			drain("SIGTERM")
		}
	}()
	served := make(chan struct{})
	go func() {
		defer close(served)
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig:  plugin.Handshake,
			VersionedPlugins: map[int]goplugin.PluginSet{plugin.ProtocolVersion: {plugin.PluginName: srv}},
			GRPCServer: func(o []grpc.ServerOption) *grpc.Server {
				if beforeHandshake != nil {
					beforeHandshake()
				}
				o = append(o, grpc.MaxRecvMsgSize(limit), grpc.MaxSendMsgSize(limit))
				s := grpc.NewServer(append(o, gate.ServerOptions()...)...)
				grpcServer.Store(s)
				return s
			},
			Logger: hclog.New(&hclog.LoggerOptions{Name: "windmill-lite", Output: stderr, Level: hclog.Error}),
		})
	}()
	// The opener only opens. This goroutine owns what it opens: it closes
	// the runtime before the engine exits, and makes it serve only once
	// that Close is deferred.
	opened := make(chan openResult, 1)
	go func() {
		select {
		case err := <-handshake:
			if err != nil {
				opened <- openResult{err: err}
				return
			}
		case <-served:
			// go-plugin did not serve: nothing opens.
			opened <- openResult{}
			return
		}
		if beforeOpen != nil {
			beforeOpen()
		}
		rt, err := wl.Open(cfg.Bundle, opts)
		if err == nil && beforePublish != nil {
			beforePublish(served)
		}
		opened <- openResult{rt, err}
	}()
	// drainStarting drains an engine that has not served: the open gets
	// the drain's timeout to complete, and its runtime is closed then; past
	// it, the runtime ends with the process.
	drainStarting := func(r *openResult) int {
		if r == nil {
			timer, stop := drainTimer(cfg.drainTimeout())
			defer stop()
			select {
			case o := <-opened:
				r = &o
			case <-served:
				return 0
			case <-timer:
			}
		}
		if r != nil {
			if r.err != nil {
				return fail(r.err)
			}
			if r.rt != nil {
				if err := r.rt.Close(); err != nil {
					fmt.Fprintf(stderr, "windmill-lite plugin: %v\n", err)
				}
			}
		}
		gate.Close()
		stopServer(grpcServer.Load())
		return 0
	}
	select {
	case r := <-opened:
		if r.err != nil {
			// The process exits, and go-plugin's server with it.
			return fail(r.err)
		}
		if r.rt == nil {
			<-served
			break
		}
		// A drain that came with the open's result: the engine never
		// serves.
		select {
		case <-drains:
			return drainStarting(&r)
		default:
		}
		defer r.rt.Close()
		wlext.OnPoolStartFailure(r.rt, func(err error) {
			drain("the warm pool failed to start a harness process: " + err.Error())
		})
		var runner plugin.RunnerServer = host.New(host.Config{
			Runtime: r.rt, Labels: cfg.Labels, ConfigDigest: digest,
			Broker: srv.broker.Load, PluginTimeout: opts.PluginTimeout, MaxMessageBytes: limit, TokenBound: tokenBound,
			OnTokenBound: func() { drain("the engine keeps its bound of tokens that GetRun answered gone for") },
		})
		if wrapRunner != nil {
			runner = wrapRunner(runner)
		}
		if beforeServe != nil {
			beforeServe()
		}
		// A drain that has begun keeps the Gate draining: it does not serve,
		// and the drain below then ends it.
		gate.Serve(runner)
		stopRSS := make(chan struct{})
		defer close(stopRSS)
		if n := cfg.recycleRSS(); n > 0 {
			go watchRSS(n, stopRSS, drain, stderr)
		}
		select {
		case <-served:
		case <-drains:
			return drainEngine(gate, r.rt, cfg.drainTimeout(), served, grpcServer.Load(), stderr)
		}
	case <-served:
		// Shutdown while the runtime opens, or go-plugin did not serve. An
		// open that completes within openGrace is closed; past it, the
		// runtime ends with the process.
		select {
		case r := <-opened:
			if r.err != nil {
				return fail(r.err)
			}
			if r.rt != nil {
				defer r.rt.Close()
			}
		case <-time.After(openGrace):
		}
	case <-drains:
		return drainStarting(nil)
	}
	if !srv.served.Load() {
		// go-plugin returns without serving when it cannot listen or set
		// its server up, having logged why.
		return fail(errors.New("go-plugin did not start serving"))
	}
	// go-plugin's shutdown call, which the host's Kill sends, stopped the
	// server, which ends the calls in flight and so the runs that they
	// started. The deferred Close then cancels any run left (cause
	// closing).
	return 0
}

// drainEngine ends the drain of an engine whose runtime opened, the Gate
// draining already, and returns its exit status, 0. The Run calls in
// flight, then the runs that steps dispatched, go on for up to timeout in
// all. The runtime then closes, which cancels the runs left: those of Run
// calls with the cause closing, the dispatched ones as wl's close cancels
// them. Its calls get answerGrace to answer before the server stops.
// go-plugin's shutdown call (served) ends the drain at once, the deferred
// Close of the runtime then closing it.
func drainEngine(gate *host.Gate, rt *wl.Runtime, timeout time.Duration, served <-chan struct{}, s *grpc.Server, stderr io.Writer) int {
	timer, stop := drainTimer(timeout)
	defer stop()
	ended := true
	for _, w := range []struct {
		what string
		idle func() <-chan struct{}
	}{
		{"the Run calls in flight", gate.Idle},
		{"the runs that steps dispatched", func() <-chan struct{} { return wlext.DispatchIdle(rt) }},
	} {
		if !ended {
			break
		}
		idle := w.idle()
		select {
		case <-idle:
			continue
		default:
		}
		fmt.Fprintf(stderr, "windmill-lite plugin: the drain waits for %s\n", w.what)
		select {
		case <-idle:
		case <-timer:
			ended = false
		case <-served:
			return 0
		}
	}
	if ended {
		fmt.Fprintln(stderr, "windmill-lite plugin: the runs in flight have ended: closing the runtime")
	} else {
		fmt.Fprintln(stderr, "windmill-lite plugin: the drain's time is up: closing the runtime, which cancels the runs left")
	}
	if beforeClose != nil {
		beforeClose()
	}
	if err := rt.Close(); err != nil {
		fmt.Fprintf(stderr, "windmill-lite plugin: %v\n", err)
	}
	grace := time.NewTimer(answerGrace)
	defer grace.Stop()
	select {
	case <-gate.Idle():
	case <-grace.C:
		fmt.Fprintf(stderr, "windmill-lite plugin: a Run call did not answer within %s of the runtime's close\n", answerGrace)
	case <-served:
		return 0
	}
	gate.Close()
	stopServer(s)
	return 0
}

// answerGrace bounds how long a drained engine waits, after its runtime's
// close, for the Run calls in flight to answer.
const answerGrace = 2 * time.Second

// drainTimer returns the channel that fires when a drain's time d is up,
// and the function that stops it. Tests replace it.
var drainTimer = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// stopServer stops s, the gRPC server that go-plugin serves on, gracefully
// for at most stopGrace, so that the answers of the calls that returned
// reach the host, then at once: go-plugin's own streams with the host do
// not end on their own.
func stopServer(s *grpc.Server) {
	if s == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopGrace):
		s.Stop()
	}
}

// stopGrace bounds a drained engine's graceful stop of its server.
const stopGrace = time.Second

// rssSamples is how many consecutive samples of the engine's resident set
// at or over engine_recycle_rss start a drain; rssInterval is the time
// between two samples.
const (
	rssSamples  = 2
	rssInterval = time.Second
)

// rssTicker returns the channel of the memory trigger's samples, one every
// rssInterval, and the function that stops it. Tests replace it.
var rssTicker = func() (<-chan time.Time, func()) {
	t := time.NewTicker(rssInterval)
	return t.C, t.Stop
}

// rssTrigger is the engine's memory trigger: it trips once rssSamples
// consecutive samples of its resident set reach threshold.
type rssTrigger struct {
	threshold int64
	over      int
}

// sample takes one sample of the resident set and reports whether the
// trigger trips.
func (t *rssTrigger) sample(rss int64) bool {
	if rss >= t.threshold {
		t.over++
	} else {
		t.over = 0
	}
	return t.over >= rssSamples
}

// watchRSS samples the engine's resident set every rssInterval until stop
// closes, and starts a drain once the trigger of threshold trips. A sample
// that cannot be read is reported and ends the sampling.
func watchRSS(threshold int64, stop <-chan struct{}, drain func(string), stderr io.Writer) {
	t := rssTrigger{threshold: threshold}
	tick, stopTick := rssTicker()
	defer stopTick()
	for {
		select {
		case <-stop:
			return
		case <-tick:
		}
		rss, err := currentRSS()
		if err != nil {
			fmt.Fprintf(stderr, "windmill-lite plugin: engine_recycle_rss: %v\n", err)
			return
		}
		if t.sample(rss) {
			drain(fmt.Sprintf("the engine's resident set, %d bytes, reached engine_recycle_rss, %d bytes, on %d consecutive samples", rss, threshold, rssSamples))
			return
		}
	}
}

// openGrace is how long an engine whose shutdown comes while its runtime
// opens waits for the open, to close the runtime: go-plugin's Kill waits 2
// seconds for the engine to exit before it kills it, which leaves the
// Close a second.
const openGrace = time.Second

// watchHandshake puts a pipe in place of os.Stdout, to which go-plugin
// writes its handshake line before it puts a pipe of its own there. What
// the pipe receives goes on to the engine's standard output. The channel
// receives nil once a whole first line has been written there, or the
// error that kept it from there.
func watchHandshake() (<-chan error, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("the handshake's pipe: %w", err)
	}
	out := os.Stdout
	os.Stdout = w
	done := make(chan error, 1)
	go func() {
		br := bufio.NewReader(r)
		line, err := br.ReadBytes('\n')
		if err == nil {
			_, err = out.Write(line)
		}
		if err != nil {
			done <- fmt.Errorf("go-plugin's handshake line did not reach standard output: %w", err)
			return
		}
		_ = out.Sync()
		done <- nil
		_, _ = io.Copy(out, br)
	}()
	return done, nil
}

// openResult is the outcome of the runtime's open.
type openResult struct {
	rt  *wl.Runtime
	err error
}

// beforeOpen, when set, is called after go-plugin's handshake, just before
// the runtime opens. Tests use it to hold the engine in the state Starting;
// it is nil otherwise.
var beforeOpen func()

// beforePublish, when set, is called with a channel that closes at shutdown,
// between the runtime's open and its serving. Tests use it to stop the
// engine there; it is nil otherwise.
var beforePublish func(stopped <-chan struct{})

// wrapRunner, when set, wraps the Runner server that the engine serves.
// Tests use it to hold a Run call's answer; it is nil otherwise.
var wrapRunner func(plugin.RunnerServer) plugin.RunnerServer

// tokenBound is how many tokens that GetRun answered gone for the engine
// keeps before it drains; zero means host.DefaultTokenBound. Tests lower it.
var tokenBound int

// beforeServe, when set, is called just before the engine serves. Tests
// use it to hold the engine there; it is nil otherwise.
var beforeServe func()

// beforeClose, when set, is called when a drain closes the runtime. Tests
// use it to mark that moment; it is nil otherwise.
var beforeClose func()

// beforeHandshake, when set, is called as go-plugin sets its server up,
// before it writes its handshake line. Tests use it to delay the handshake;
// it is nil otherwise.
var beforeHandshake func()

// servedRunner is the Runner plugin, which notes that go-plugin set its
// server up: go-plugin prints its handshake line and serves only after
// that, and returns from Serve without either when it fails before.
type servedRunner struct {
	plugin.RunnerPlugin
	served atomic.Bool
	// broker is go-plugin's broker of the connection, on which the host
	// serves the runs' hooks.
	broker atomic.Pointer[goplugin.GRPCBroker]
}

func (p *servedRunner) GRPCServer(b *goplugin.GRPCBroker, s *grpc.Server) error {
	if err := p.RunnerPlugin.GRPCServer(b, s); err != nil {
		return err
	}
	p.broker.Store(b)
	p.served.Store(true)
	return nil
}

// afterBundleCheck, when set, is called with the bundle's path between its
// check and the runtime's open. Tests use it to replace the bundle there;
// it is nil otherwise.
var afterBundleCheck func(bundle string)

// pluginConfig is the configuration of `windmill-lite plugin` (ADR 0009,
// the plugin command).
type pluginConfig struct {
	Bundle                 string                    `json:"bundle"`
	Digest                 string                    `json:"digest"`
	Bun                    string                    `json:"bun"`
	HarnessRuntime         string                    `json:"harness_runtime"`
	DataDir                string                    `json:"data_dir"`
	Resources              map[string]configResource `json:"resources"`
	Variables              map[string]configVariable `json:"variables"`
	ResourceStore          *configStore              `json:"resource_store"`
	Identity               json.RawMessage           `json:"identity"`
	Providers              map[string]configProvider `json:"providers"`
	PluginTimeoutMS        *int64                    `json:"plugin_timeout_ms"`
	ReadCache              bool                      `json:"read_cache"`
	Pool                   *configPool               `json:"pool"`
	ResultPollsForAllJobs  bool                      `json:"result_polls_for_all_jobs"`
	ForeignWindmillOrigins []string                  `json:"foreign_windmill_origins"`
	EngineRecycleRSS       *int64                    `json:"engine_recycle_rss"`
	DrainTimeoutMS         *int64                    `json:"drain_timeout_ms"`
	MaxMessageBytes        *int64                    `json:"max_message_bytes"`
	BytesTotal             *int64                    `json:"bytes_total"`
	Labels                 map[string]string         `json:"labels"`
}

// configResource is a resource of the configuration, as in a bundle's
// data/resources.json.
type configResource struct {
	ResourceType string          `json:"resource_type"`
	Value        json.RawMessage `json:"value"`
}

// configVariable is a variable of the configuration, as in a bundle's
// data/variables.json, whose value may instead name a variable of the
// engine's environment (value_env). A secret must.
type configVariable struct {
	Value     *string `json:"value"`
	ValueEnv  string  `json:"value_env"`
	IsSecret  bool    `json:"is_secret"`
	Encrypted bool    `json:"encrypted"`
}

// configStore is the resource store: memory, or Redis, whose URL names a
// variable of the engine's environment (url_env).
type configStore struct {
	Kind          string  `json:"kind"`
	URL           *string `json:"url"`
	URLEnv        string  `json:"url_env"`
	Prefix        string  `json:"prefix"`
	AllowEviction bool    `json:"allow_eviction"`
}

// configProvider is a named provider: a static value, or the JSON string
// "broker" for the host's hooks service.
type configProvider struct {
	Broker bool
	Value  json.RawMessage
}

func (p *configProvider) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s != "broker" {
			return fmt.Errorf("%q is neither broker nor an object with a value", s)
		}
		p.Broker = true
		return nil
	}
	var v struct {
		Value json.RawMessage `json:"value"`
	}
	if err := strictJSON(b, &v); err != nil {
		return err
	}
	if v.Value == nil {
		return errors.New("an object with a value, which the provider answers")
	}
	p.Value = v.Value
	return nil
}

// configPool is the pool of a warm runtime (wl.PoolOptions).
type configPool struct {
	Min              int            `json:"min"`
	Max              int            `json:"max"`
	Cap              int            `json:"cap"`
	LagThresholdMS   int64          `json:"lag_threshold_ms"`
	RecycleTimeoutMS int64          `json:"recycle_timeout_ms"`
	Recycle          *configRecycle `json:"recycle"`
	MaxLive          *int           `json:"max_live"`
	PinRuns          *bool          `json:"pin_runs"`
	WarmStart        bool           `json:"warm_start"`
}

// configRecycle are a pool's recycle triggers (wl.RecycleTriggers).
type configRecycle struct {
	RSS     int64 `json:"rss"`
	Samples int   `json:"samples"`
	Modules int   `json:"modules"`
}

// strictJSON decodes exactly one JSON value into v, refusing a field that
// v does not have.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("more than one JSON value")
	}
	return nil
}

// decodePluginConfig decodes a configuration and checks what it can
// without the environment or the bundle: a field that it does not know, a
// field that this engine does not serve yet, a secret written in the file
// and a value out of range each refuse it, naming the field and never a
// value.
func decodePluginConfig(data []byte) (*pluginConfig, error) {
	var c pluginConfig
	if err := strictJSON(data, &c); err != nil {
		return nil, err
	}
	if c.Bundle == "" {
		return nil, errors.New("bundle: required")
	}
	for _, path := range slices.Sorted(maps.Keys(c.Variables)) {
		v := c.Variables[path]
		switch {
		case v.Value != nil && v.ValueEnv != "":
			return nil, fmt.Errorf("variables[%q]: value and value_env are exclusive", path)
		case v.IsSecret && v.Value != nil:
			return nil, fmt.Errorf("variables[%q].value: a secret is never written in the file; name a variable of the engine's environment with value_env", path)
		case v.Value == nil && v.ValueEnv == "":
			return nil, fmt.Errorf("variables[%q]: value or value_env is required", path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(c.Resources)) {
		if r := c.Resources[path]; !json.Valid(r.Value) {
			return nil, fmt.Errorf("resources[%q].value: not JSON", path)
		}
	}
	if s := c.ResourceStore; s != nil {
		switch {
		case s.URL != nil:
			return nil, errors.New("resource_store.url: the store's URL may hold a credential, so it is never written in the file; name a variable of the engine's environment with url_env")
		case s.Kind == "memory" && (s.URLEnv != "" || s.Prefix != "" || s.AllowEviction):
			return nil, errors.New("resource_store: a memory store takes no url_env, prefix or allow_eviction")
		case s.Kind == "redis" && s.URLEnv == "":
			return nil, errors.New("resource_store.url_env: required for a redis store")
		case s.Kind != "memory" && s.Kind != "redis":
			return nil, fmt.Errorf("resource_store.kind: %q is neither memory nor redis", s.Kind)
		}
	}
	if len(c.Identity) > 0 {
		if _, err := wl.DecodeIdentity(c.Identity); err != nil {
			return nil, err
		}
	}
	if c.EngineRecycleRSS != nil && *c.EngineRecycleRSS < 0 {
		return nil, errors.New("engine_recycle_rss: not 0 or more")
	}
	if c.DrainTimeoutMS != nil && (*c.DrainTimeoutMS < 0 || *c.DrainTimeoutMS > maxMS) {
		return nil, fmt.Errorf("drain_timeout_ms: not between 0 and %d", maxMS)
	}
	if c.PluginTimeoutMS != nil && (*c.PluginTimeoutMS <= 0 || *c.PluginTimeoutMS > maxMS) {
		return nil, fmt.Errorf("plugin_timeout_ms: not between 1 and %d", maxMS)
	}
	if p := c.Pool; p != nil {
		if p.LagThresholdMS < -maxMS || p.LagThresholdMS > maxMS {
			return nil, fmt.Errorf("pool.lag_threshold_ms: not between %d and %d", -maxMS, maxMS)
		}
		if p.RecycleTimeoutMS < -maxMS || p.RecycleTimeoutMS > maxMS {
			return nil, fmt.Errorf("pool.recycle_timeout_ms: not between %d and %d", -maxMS, maxMS)
		}
	}
	if c.MaxMessageBytes != nil && (*c.MaxMessageBytes <= 0 || *c.MaxMessageBytes > int64(maxInt)) {
		return nil, errors.New("max_message_bytes: not a positive int")
	}
	return &c, nil
}

// maxInt is the largest int.
const maxInt = int(^uint(0) >> 1)

// defaultDrainTimeout is how long a drain lets the runs in flight go on
// when the configuration sets no drain_timeout_ms.
const defaultDrainTimeout = time.Minute

// drainTimeout is how long a drain lets the runs in flight go on.
func (c *pluginConfig) drainTimeout() time.Duration {
	if c.DrainTimeoutMS == nil {
		return defaultDrainTimeout
	}
	return time.Duration(*c.DrainTimeoutMS) * time.Millisecond
}

// recycleRSS is the engine's resident set, in bytes, at which it drains;
// zero for none.
func (c *pluginConfig) recycleRSS() int64 {
	if c.EngineRecycleRSS == nil {
		return 0
	}
	return *c.EngineRecycleRSS
}

// maxMS is the largest number of milliseconds that a time.Duration holds:
// 9,223,372,036,854 ms, about 292 years. A setting in milliseconds beyond
// it, either way, would change sign as a duration.
const maxMS = math.MaxInt64 / int64(time.Millisecond)

// checkBundle verifies the bundle against its manifest and the digest pin,
// and returns the digest that passed, which the runtime's open then pins.
// A host plugin that the manifest names loads with the bundle, as wl.Open
// loads it.
func (c *pluginConfig) checkBundle() (string, error) {
	v, err := bundle.Verify(c.Bundle, bundle.VerifyOptions{Digest: c.Digest})
	if err != nil {
		return "", err
	}
	return v.Digest, nil
}

// maxMessageBytes is the largest gRPC message in either direction.
func (c *pluginConfig) maxMessageBytes() int {
	if c.MaxMessageBytes == nil {
		return plugin.DefaultMaxMessageBytes
	}
	return int(*c.MaxMessageBytes)
}

// options are the runtime's wl.Options and the function that closes its
// resource store. Each variable of the environment that the configuration
// names is read here, once, and then unset, so no process that the engine
// starts inherits it; one that is not set fails the start, naming it.
func (c *pluginConfig) options(stderr io.Writer) (wl.Options, func(), error) {
	opts := wl.Options{
		Bun:                    c.Bun,
		HarnessRuntime:         c.HarnessRuntime,
		DataDir:                c.DataDir,
		Digest:                 c.Digest,
		EngineVersion:          resolveVersion(),
		Diagnostics:            stderr,
		ReadCache:              c.ReadCache,
		ResultPollsForAllJobs:  c.ResultPollsForAllJobs,
		ForeignWindmillOrigins: c.ForeignWindmillOrigins,
	}
	if c.BytesTotal != nil {
		opts.Limits.Bytes = *c.BytesTotal
	}
	if c.PluginTimeoutMS != nil {
		opts.PluginTimeout = time.Duration(*c.PluginTimeoutMS) * time.Millisecond
	}
	if len(c.Identity) > 0 {
		id, err := wl.DecodeIdentity(c.Identity)
		if err != nil {
			return wl.Options{}, nil, err
		}
		opts.Identity = id
	}
	if len(c.Resources) > 0 {
		opts.Resources = make(map[string]wl.Resource, len(c.Resources))
		for path, r := range c.Resources {
			opts.Resources[path] = wl.Resource{Type: r.ResourceType, Value: r.Value}
		}
	}
	env := envReader{}
	defer env.unset()
	if len(c.Variables) > 0 {
		opts.Variables = make(map[string]wl.Variable, len(c.Variables))
		for _, path := range slices.Sorted(maps.Keys(c.Variables)) {
			v := c.Variables[path]
			value := ""
			if v.Value != nil {
				value = *v.Value
			} else {
				var err error
				if value, err = env.read(v.ValueEnv); err != nil {
					return wl.Options{}, nil, fmt.Errorf("variables[%q].value_env: %w", path, err)
				}
			}
			opts.Variables[path] = wl.Variable{Value: value, Secret: v.IsSecret, Ciphertext: v.Encrypted}
		}
	}
	if len(c.Providers) > 0 {
		opts.Providers = make(map[string]wl.Provider, len(c.Providers))
		var broker []string
		for name, p := range c.Providers {
			if p.Broker {
				broker = append(broker, name)
				continue
			}
			value := p.Value
			opts.Providers[name] = func(context.Context, wl.Call) (wl.Outcome, bool, error) {
				return wl.Outcome{Result: value}, true, nil
			}
		}
		// A provider set to broker is answered by the hooks of the run that
		// reaches it, on the host's broker connection.
		wlext.BrokerProviders(&opts, broker)
	}
	if p := c.Pool; p != nil {
		opts.Pool = wl.PoolOptions{
			Min: p.Min, Max: p.Max, Cap: p.Cap, WarmStart: p.WarmStart,
			LagThreshold:   time.Duration(p.LagThresholdMS) * time.Millisecond,
			RecycleTimeout: time.Duration(p.RecycleTimeoutMS) * time.Millisecond,
		}
		if r := p.Recycle; r != nil {
			opts.Pool.Recycle = wl.RecycleTriggers{RSS: r.RSS, Samples: r.Samples, Modules: r.Modules}
		}
		if p.MaxLive != nil {
			opts.Pool.MaxLive = *p.MaxLive
		}
		if p.PinRuns != nil {
			opts.Pool.PinRuns = *p.PinRuns
		}
	}
	closeStore := func() {}
	if s := c.ResourceStore; s != nil && s.Kind == "redis" {
		url, err := env.read(s.URLEnv)
		if err != nil {
			return wl.Options{}, nil, fmt.Errorf("resource_store.url_env: %w", err)
		}
		prefix := s.Prefix
		if prefix == "" {
			prefix = redisstore.DefaultPrefix
		}
		store, err := redisstore.New(context.Background(), redisstore.Options{
			URL: url, Prefix: prefix, AllowEviction: s.AllowEviction,
			Warn: func(m string) { fmt.Fprintln(stderr, "windmill-lite:", m) },
		})
		if err != nil {
			return wl.Options{}, nil, fmt.Errorf("resource_store: %w", err)
		}
		opts.ResourceStore = store
		closeStore = func() { store.Close() }
	}
	return opts, closeStore, nil
}

// envReader reads the variables of the engine's environment that the
// configuration names, each once, however many references name it, and
// unsets each that it read, so no process that the engine starts inherits
// it.
type envReader map[string]string

// read returns the value of the variable name. The error names the
// variable, never a value.
func (e envReader) read(name string) (string, error) {
	if v, ok := e[name]; ok {
		return v, nil
	}
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("the environment variable %s is not set", strconv.Quote(name))
	}
	e[name] = v
	return v, nil
}

// unset unsets every variable that read read.
func (e envReader) unset() {
	for name := range e {
		os.Unsetenv(name)
	}
}
