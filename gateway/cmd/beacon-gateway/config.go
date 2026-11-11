package main

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/envoy"
	"github.com/asymptote-labs/agent-beacon/gateway/events"
	"github.com/asymptote-labs/agent-beacon/gateway/litellm"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
	"github.com/asymptote-labs/agent-beacon/gateway/portkey"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// defaultConfig is used when no -config is given: the LiteLLM callout, records in the working
// directory.
//
//go:embed default.yaml
var defaultConfig []byte

// debug turns on profiling. pprof is served on the admin listener; the rates make the mutex and
// block profiles record anything (both cost a little on every contended lock or blocking call).
type debug struct {
	Pprof bool `yaml:"pprof"`
	// MutexProfileFraction samples one in n contended-mutex events (0: off).
	MutexProfileFraction int `yaml:"mutex_profile_fraction"`
	// BlockProfileRate samples blocking events lasting at least this many nanoseconds (0: off).
	BlockProfileRate int `yaml:"block_profile_rate"`
}

func (d debug) apply() {
	runtime.SetMutexProfileFraction(d.MutexProfileFraction)
	runtime.SetBlockProfileRate(d.BlockProfileRate)
}

// config is the configuration file.
type config struct {
	// Admin serves /healthz and /stats (and pprof when debug.pprof); "off" disables it.
	Admin    string `yaml:"admin"`
	Debug    debug  `yaml:"debug"`
	GC       gc     `yaml:"gc"`
	Pipeline struct {
		SessionHeaders []string      `yaml:"session_headers"`
		SeenTTL        time.Duration `yaml:"seen_ttl"`
		// MaxBodyBytes bounds the bytes of call bodies read and analyzed at once ("1GiB"; default
		// a quarter of the GC memory limit, else 1GiB); MaxInFlight bounds the calls analyzed at
		// once (default 2 per CPU). A call waits up to QueueWait for either before it is refused.
		MaxBodyBytes string        `yaml:"max_body_bytes"`
		MaxInFlight  int           `yaml:"max_in_flight"`
		QueueWait    time.Duration `yaml:"queue_wait"`
	} `yaml:"pipeline"`
	// Content is what recorded events keep of the content they describe (events.Config).
	Content events.Config `yaml:"content"`
	// Store holds every piece of cross-call state; its type picks the implementation, "memory"
	// by default.
	Store yaml.Node `yaml:"store"`
	// Analyzers, Policy, Observers and Outputs configure extensions a build registers (extend.go);
	// outputs also take the built-in jsonl type.
	Analyzers map[string]yaml.Node `yaml:"analyzers"`
	Policy    yaml.Node            `yaml:"policy"`
	Observers map[string]yaml.Node `yaml:"observers"`
	Outputs   []yaml.Node          `yaml:"outputs"`
	// Gateways each have a type naming the adapter; the rest of the entry is that adapter's
	// Config.
	Gateways []yaml.Node `yaml:"gateways"`
}

// adapters builds each gateway type from its config entry. The table is the whole registry:
// adding a gateway is one package and one line here.
var adapters = map[string]func(*yaml.Node, *slog.Logger) (gateway.Adapter, error){
	"litellm": adapter(litellm.New),
	"envoy":   adapter(envoy.New),
	"portkey": adapter(portkey.New),
}

func adapter[C any](build func(C, *slog.Logger) (gateway.Adapter, error)) func(*yaml.Node, *slog.Logger) (gateway.Adapter, error) {
	return func(n *yaml.Node, log *slog.Logger) (gateway.Adapter, error) {
		var cfg C
		if err := n.Decode(&cfg); err != nil {
			return nil, err
		}
		return build(cfg, log)
	}
}

func loadConfig(path string) (*config, error) {
	data := defaultConfig
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var cfg config
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(data))), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Admin == "" {
		cfg.Admin = "127.0.0.1:4029"
	}
	if len(cfg.Gateways) == 0 {
		return nil, fmt.Errorf("config: no gateways")
	}
	return &cfg, nil
}

// present reports whether a configuration node was given.
func present(n *yaml.Node) bool { return n.Kind != 0 }

func (cfg *config) store(env Env) (store.Store, error) {
	var head struct {
		Type string `yaml:"type"`
	}
	var node *yaml.Node
	if present(&cfg.Store) {
		node = &cfg.Store
		if err := node.Decode(&head); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	if head.Type == "" {
		head.Type = "memory"
	}
	build, ok := storeFactories[head.Type]
	if !ok {
		return nil, fmt.Errorf("store.type: unknown store %q", head.Type)
	}
	s, err := build(node, env)
	if err != nil {
		return nil, fmt.Errorf("store (%s): %w", head.Type, err)
	}
	return s, nil
}

// analyzers builds the events analyzer, then every configured extension analyzer, in name order.
func (cfg *config) analyzers(env Env) ([]pipeline.Analyzer, error) {
	ec := cfg.Content
	ec.Version = env.Version
	ev, err := events.New(ec)
	if err != nil {
		return nil, err
	}
	out := []pipeline.Analyzer{ev}
	for _, name := range sortedNames(cfg.Analyzers) {
		build, ok := analyzerFactories[name]
		if !ok {
			return nil, fmt.Errorf("analyzers.%s: no such analyzer in this build", name)
		}
		n := cfg.Analyzers[name]
		a, err := build(&n, env)
		if err != nil {
			return nil, fmt.Errorf("analyzers.%s: %w", name, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// policy builds the configured policy; nil, the default, allows every call.
func (cfg *config) policy(env Env) (pipeline.Policy, error) {
	if !present(&cfg.Policy) {
		if policyFactory == nil {
			return nil, nil
		}
	} else if policyFactory == nil {
		return nil, fmt.Errorf("policy: this build has no configurable policy")
	}
	p, err := policyFactory(&cfg.Policy, env)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return p, nil
}

func (cfg *config) observers(env Env) ([]pipeline.Observer, error) {
	var out []pipeline.Observer
	for _, name := range sortedNames(cfg.Observers) {
		build, ok := observerFactories[name]
		if !ok {
			return nil, fmt.Errorf("observers.%s: no such observer in this build", name)
		}
		n := cfg.Observers[name]
		o, err := build(&n, env)
		if err != nil {
			return nil, fmt.Errorf("observers.%s: %w", name, err)
		}
		out = append(out, o)
	}
	return out, nil
}

func sortedNames(m map[string]yaml.Node) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// routes builds every output: a sink of the type its entry names, fed the record kinds it lists
// (every kind when it lists none). An entry with only a jsonl path is a jsonl output. A route is
// named by the entry's name, a jsonl output by its path, and anything else by type and position,
// which is how /stats reports what each one dropped. Two jsonl outputs may not share a file: each
// cuts a failed write back out of its file, which would cut the other's records.
func (cfg *config) routes(env Env) ([]pipeline.Route, error) {
	var out []pipeline.Route
	fail := func(err error) ([]pipeline.Route, error) {
		for _, r := range out {
			r.Sink.Close()
		}
		return nil, err
	}
	files := map[string]int{}
	for i := range cfg.Outputs {
		n := &cfg.Outputs[i]
		var head struct {
			Type  string   `yaml:"type"`
			Name  string   `yaml:"name"`
			JSONL string   `yaml:"jsonl"`
			Kinds []string `yaml:"kinds"`
		}
		if err := n.Decode(&head); err != nil {
			return fail(fmt.Errorf("outputs[%d]: %w", i, err))
		}
		if head.Type == "" && head.JSONL != "" {
			head.Type = "jsonl"
		}
		if head.Type == "" {
			return fail(fmt.Errorf("outputs[%d]: no type (or jsonl path)", i))
		}
		if head.Type == "jsonl" && head.JSONL != "" {
			path, err := filepath.Abs(expandHome(head.JSONL))
			if err != nil {
				return fail(fmt.Errorf("outputs[%d]: %w", i, err))
			}
			if j, dup := files[path]; dup {
				return fail(fmt.Errorf("outputs[%d]: %s is also outputs[%d]'s file; give each jsonl output a file of its own (one output can take several kinds)", i, head.JSONL, j))
			}
			files[path] = i
		}
		build, ok := sinkFactories[head.Type]
		if !ok {
			return fail(fmt.Errorf("outputs[%d]: no such output %q in this build", i, head.Type))
		}
		s, err := build(n, env)
		if err != nil {
			return fail(fmt.Errorf("outputs[%d] (%s): %w", i, head.Type, err))
		}
		name := head.Name
		switch {
		case name != "":
		case head.Type == "jsonl":
			name = head.JSONL
		default:
			name = fmt.Sprintf("%s[%d]", head.Type, i)
		}
		out = append(out, pipeline.Route{Name: name, Sink: s, Kinds: head.Kinds})
	}
	return out, nil
}

func (cfg *config) gateways(log *slog.Logger) ([]gateway.Adapter, error) {
	out := make([]gateway.Adapter, 0, len(cfg.Gateways))
	for i := range cfg.Gateways {
		n := &cfg.Gateways[i]
		var head struct {
			Type string `yaml:"type"`
		}
		if err := n.Decode(&head); err != nil {
			return nil, fmt.Errorf("gateways[%d]: %w", i, err)
		}
		build, ok := adapters[head.Type]
		if !ok {
			return nil, fmt.Errorf("gateways[%d]: unknown type %q", i, head.Type)
		}
		a, err := build(n, log)
		if err != nil {
			return nil, fmt.Errorf("gateways[%d] (%s): %w", i, head.Type, err)
		}
		out = append(out, a)
	}
	return out, nil
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}
