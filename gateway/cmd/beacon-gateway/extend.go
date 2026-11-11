package main

import (
	"fmt"
	"log/slog"

	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
	"github.com/asymptote-labs/agent-beacon/gateway/sink"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// Extension points. A separately distributed build compiles in files of its own that register
// analyzers, a policy, stores, call observers and outputs here from init functions; the open
// build registers nothing beyond the in-memory store and the jsonl output, so every call is
// allowed and recorded. Each extension reads its own node of the configuration file, which the
// open parser passes through without looking inside:
//
//	analyzers:            # each entry runs after the events analyzer, in name order
//	  <name>: {...}       # → analyzerFactories[name]
//	policy: {...}         # → policyFactory
//	store:
//	  type: <type>        # → storeFactories[type], given the whole store node
//	observers:
//	  <name>: {...}       # → observerFactories[name]
//	outputs:
//	  - type: <type>      # → sinkFactories[type], given the whole entry; `jsonl: <path>` alone
//	    kinds: [...]      #   is a jsonl output. kinds and name are read by the open parser.
//
// A name the build did not register is a configuration error, as is a policy node in a build with
// no policy factory.
var (
	analyzerFactories = map[string]AnalyzerFactory{}
	// policyFactory builds the policy; nil leaves the default, which allows every call.
	policyFactory     PolicyFactory
	storeFactories    = map[string]StoreFactory{"memory": memoryStore}
	observerFactories = map[string]ObserverFactory{}
	sinkFactories     = map[string]SinkFactory{"jsonl": jsonlSink}
)

// Env is what every factory is given.
type Env struct {
	Log *slog.Logger
	// Version is the gateway's version.
	Version string
}

// AnalyzerFactory builds an analyzer from its configuration node. The analyzer may also implement
// pipeline.Background, pipeline.StatsReporter, pipeline.Inline and pipeline.ResentReader.
type AnalyzerFactory func(node *yaml.Node, env Env) (pipeline.Analyzer, error)

// PolicyFactory builds the policy from its configuration node.
type PolicyFactory func(node *yaml.Node, env Env) (pipeline.Policy, error)

// StoreFactory builds the cross-call store from the store node, type included.
type StoreFactory func(node *yaml.Node, env Env) (store.Store, error)

// ObserverFactory builds a per-call observer from its configuration node.
type ObserverFactory func(node *yaml.Node, env Env) (pipeline.Observer, error)

// SinkFactory builds an output's sink from its entry, type and kinds included. The sink receives
// the records of the entry's kinds in batches, off the call path (pipeline.Sink).
type SinkFactory func(node *yaml.Node, env Env) (pipeline.Sink, error)

// jsonlSink is the built-in output: records appended to a local file, one JSON object per line.
func jsonlSink(node *yaml.Node, _ Env) (pipeline.Sink, error) {
	var cfg struct {
		JSONL string `yaml:"jsonl"`
	}
	if err := node.Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.JSONL == "" {
		return nil, fmt.Errorf("a jsonl output needs a path (jsonl: <path>)")
	}
	s, err := sink.OpenJSONL(expandHome(cfg.JSONL))
	if err != nil {
		return nil, err
	}
	return s, nil
}

// memoryStore is the built-in store: one process's memory, bounded at max_entries keys.
func memoryStore(node *yaml.Node, _ Env) (store.Store, error) {
	var cfg struct {
		MaxEntries int `yaml:"max_entries"`
	}
	if node != nil {
		if err := node.Decode(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 1_000_000
	}
	return store.NewMemory(cfg.MaxEntries), nil
}
