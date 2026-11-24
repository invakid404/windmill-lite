package main

import (
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
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/host"
	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/plugin"
	"github.com/invakid404/windmill-lite/wl/redisstore"
)

const pluginUsage = "usage: windmill-lite plugin -config <file>"

// runPlugin is `windmill-lite plugin -config <file>` (ADR 0009, the
// go-plugin adapter): the engine as a go-plugin plugin, which a Go host
// launches with wl/plugin. It reads its configuration, opens the bundle
// with wl, with the pool's warm start when the configuration asks for it,
// and only then serves windmill_lite.v1.Runner until its host stops it. A
// start that fails writes its reason to standard error, which go-plugin
// passes to the host's logger, and exits with status 1.
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
	// go-plugin's handshake would refuse it after the runtime has opened.
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
	rt, err := wl.Open(cfg.Bundle, opts)
	if err != nil {
		return fail(err)
	}
	defer rt.Close()
	limit := cfg.maxMessageBytes()
	srv := &servedRunner{RunnerPlugin: plugin.RunnerPlugin{Runner: host.New(host.Config{Runtime: rt, Labels: cfg.Labels, ConfigDigest: digest})}}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig:  plugin.Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{plugin.ProtocolVersion: {plugin.PluginName: srv}},
		GRPCServer: func(o []grpc.ServerOption) *grpc.Server {
			return grpc.NewServer(append(o, grpc.MaxRecvMsgSize(limit), grpc.MaxSendMsgSize(limit))...)
		},
		Logger: hclog.New(&hclog.LoggerOptions{Name: "windmill-lite", Output: stderr, Level: hclog.Error}),
	})
	if !srv.served.Load() {
		// go-plugin returns without serving when it cannot listen or set
		// its server up, having logged why.
		return fail(errors.New("go-plugin did not start serving"))
	}
	// go-plugin's shutdown call, which the host's Kill sends, stopped the
	// server, which ends the calls in flight and so the runs that they
	// started. The deferred Close then cancels any run left (cause closing).
	return 0
}

// servedRunner is the Runner plugin, which notes that go-plugin set its
// server up: go-plugin prints its handshake line and serves only after
// that, and returns from Serve without either when it fails before.
type servedRunner struct {
	plugin.RunnerPlugin
	served atomic.Bool
}

func (p *servedRunner) GRPCServer(b *goplugin.GRPCBroker, s *grpc.Server) error {
	if err := p.RunnerPlugin.GRPCServer(b, s); err != nil {
		return err
	}
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
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"engine_recycle_rss", c.EngineRecycleRSS != nil},
		{"drain_timeout_ms", c.DrainTimeoutMS != nil},
	} {
		if f.set {
			return nil, fmt.Errorf("%s: this engine does not serve it yet", f.name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Providers)) {
		if c.Providers[name].Broker {
			return nil, fmt.Errorf("providers[%q]: this engine does not serve broker providers yet", name)
		}
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
		for name, p := range c.Providers {
			value := p.Value
			opts.Providers[name] = func(context.Context, wl.Call) (wl.Outcome, bool, error) {
				return wl.Outcome{Result: value}, true, nil
			}
		}
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
