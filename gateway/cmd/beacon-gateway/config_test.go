package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

func parse(t *testing.T, text string) *config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

var env = Env{Log: slog.New(slog.DiscardHandler), Version: "test"}

// Two jsonl outputs may not write one file, however the path is spelled.
func TestOutputsShareNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := parse(t, "gateways: [{type: litellm}]\noutputs:\n  - {jsonl: "+dir+"/a.jsonl, kinds: [finding]}\n  - {jsonl: "+dir+"/x/../a.jsonl, kinds: [beacon.event]}\n")
	if _, err := cfg.routes(env); err == nil || !strings.Contains(err.Error(), "also outputs[0]'s file") {
		t.Errorf("routes = %v, want the shared file refused", err)
	}
	cfg = parse(t, "gateways: [{type: litellm}]\noutputs:\n  - {jsonl: "+dir+"/a.jsonl}\n  - {jsonl: "+dir+"/b.jsonl}\n")
	routes, err := cfg.routes(env)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes = %v, %v", routes, err)
	}
	for _, r := range routes {
		r.Sink.Close()
	}
}

// The open build runs the events analyzer alone, with the default policy, the in-memory store and
// no observers; it rejects configuration for extensions it does not have.
func TestOpenBuild(t *testing.T) {
	cfg := parse(t, "gateways: [{type: litellm}]\n")
	if as, err := cfg.analyzers(env); err != nil || len(as) != 1 || as[0].Name() != "events" {
		t.Errorf("analyzers %v, %v", as, err)
	}
	if p, err := cfg.policy(env); p != nil || err != nil {
		t.Errorf("policy %v, %v", p, err)
	}
	if s, err := cfg.store(env); err != nil {
		t.Error(err)
	} else if _, ok := s.(*store.Memory); !ok {
		t.Errorf("store %T", s)
	}
	if obs, err := cfg.observers(env); obs != nil || err != nil {
		t.Errorf("observers %v, %v", obs, err)
	}
	// The jsonl output, written as its path alone or with its type, is the open build's only one.
	dir := t.TempDir()
	cfg = parse(t, "gateways: [{type: litellm}]\noutputs:\n  - jsonl: "+filepath.Join(dir, "a.jsonl")+"\n    kinds: [finding]\n  - {type: jsonl, jsonl: "+filepath.Join(dir, "b.jsonl")+", name: second}\n")
	routes, err := cfg.routes(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].Name != filepath.Join(dir, "a.jsonl") || len(routes[0].Kinds) != 1 || routes[1].Name != "second" {
		t.Errorf("routes %+v", routes)
	}
	for _, r := range routes {
		r.Sink.Close()
	}
	for text, want := range map[string]string{
		"analyzers: {rules: {path: x}}": "analyzers.rules: no such analyzer",
		"policy: {anything: 1}":         "policy: this build has no configurable policy",
		"store: {type: shared}":         `unknown store "shared"`,
		"observers: {calls: {}}":        "observers.calls: no such observer",
		"content: {retention: some}":    "content.retention",
		"outputs: [{type: ingest}]":     `outputs[0]: no such output "ingest"`,
		"outputs: [{kinds: [finding]}]": "outputs[0]: no type",
		"outputs: [{type: jsonl}]":      "a jsonl output needs a path",
	} {
		cfg := parse(t, "gateways: [{type: litellm}]\n"+text+"\n")
		_, aerr := cfg.analyzers(env)
		_, perr := cfg.policy(env)
		_, serr := cfg.store(env)
		_, oerr := cfg.observers(env)
		_, rerr := cfg.routes(env)
		var msgs []string
		for _, err := range []error{aerr, perr, serr, oerr, rerr} {
			if err != nil {
				msgs = append(msgs, err.Error())
			}
		}
		if len(msgs) != 1 || !strings.Contains(msgs[0], want) {
			t.Errorf("%s: errors %v, want one with %q", text, msgs, want)
		}
	}
}

type namedAnalyzer struct {
	name string
	node map[string]any
}

func (a namedAnalyzer) Name() string { return a.name }
func (namedAnalyzer) Analyze(context.Context, *pipeline.Turn) (pipeline.Report, error) {
	return pipeline.Report{}, nil
}

type nodePolicy struct {
	pipeline.AllowAll
	node map[string]any
}

type nodeObserver struct{ node map[string]any }

func (nodeObserver) Observe(*pipeline.Observation) []pipeline.Record { return nil }

type nodeStore struct {
	store.Store
	node map[string]any
}

type nodeSink struct{ node map[string]any }

func (nodeSink) Write(context.Context, []pipeline.Record) error { return nil }
func (nodeSink) Close() error                                   { return nil }

func decode(t *testing.T, n *yaml.Node) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := n.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A build registers extensions in the package-level tables; each is built from its own node of the
// configuration, which the open parser passes through whole, and analyzers run after the events
// analyzer in name order.
func TestExtensionPoints(t *testing.T) {
	saved := [5]any{analyzerFactories, policyFactory, storeFactories, observerFactories, sinkFactories}
	t.Cleanup(func() {
		analyzerFactories = saved[0].(map[string]AnalyzerFactory)
		policyFactory, _ = saved[1].(PolicyFactory)
		storeFactories = saved[2].(map[string]StoreFactory)
		observerFactories = saved[3].(map[string]ObserverFactory)
		sinkFactories = saved[4].(map[string]SinkFactory)
	})
	analyzer := func(name string) AnalyzerFactory {
		return func(n *yaml.Node, _ Env) (pipeline.Analyzer, error) {
			return namedAnalyzer{name: name, node: decode(t, n)}, nil
		}
	}
	analyzerFactories = map[string]AnalyzerFactory{"zeta": analyzer("zeta"), "alpha": analyzer("alpha")}
	policyFactory = func(n *yaml.Node, _ Env) (pipeline.Policy, error) { return nodePolicy{node: decode(t, n)}, nil }
	storeFactories = map[string]StoreFactory{"memory": memoryStore, "shared": func(n *yaml.Node, e Env) (store.Store, error) {
		return nodeStore{Store: store.NewMemory(10), node: decode(t, n)}, nil
	}}
	observerFactories = map[string]ObserverFactory{"calls": func(n *yaml.Node, _ Env) (pipeline.Observer, error) {
		return nodeObserver{node: decode(t, n)}, nil
	}}
	sinkFactories = map[string]SinkFactory{"jsonl": jsonlSink, "ingest": func(n *yaml.Node, _ Env) (pipeline.Sink, error) {
		return nodeSink{node: decode(t, n)}, nil
	}}

	cfg := parse(t, `
gateways: [{type: litellm}]
analyzers:
  zeta: {level: 2}
  alpha: {level: 1}
policy: {mode: strict}
store: {type: shared, addr: "host:1"}
observers:
  calls: {keep: true}
outputs:
  - {type: ingest, url: "https://ingest.example", kinds: [beacon.event]}
`)
	as, err := cfg.analyzers(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 3 || as[0].Name() != "events" || as[1].Name() != "alpha" || as[2].Name() != "zeta" ||
		as[1].(namedAnalyzer).node["level"] != 1 || as[2].(namedAnalyzer).node["level"] != 2 {
		t.Errorf("analyzers %+v", as)
	}
	if p, err := cfg.policy(env); err != nil || p.(nodePolicy).node["mode"] != "strict" {
		t.Errorf("policy %+v, %v", p, err)
	}
	if s, err := cfg.store(env); err != nil || s.(nodeStore).node["addr"] != "host:1" {
		t.Errorf("store %+v, %v", s, err)
	}
	if o, err := cfg.observers(env); err != nil || len(o) != 1 || o[0].(nodeObserver).node["keep"] != true {
		t.Errorf("observers %+v, %v", o, err)
	}
	if r, err := cfg.routes(env); err != nil || len(r) != 1 || r[0].Name != "ingest[0]" ||
		r[0].Sink.(nodeSink).node["url"] != "https://ingest.example" || len(r[0].Kinds) != 1 || r[0].Kinds[0] != "beacon.event" {
		t.Errorf("routes %+v, %v", r, err)
	}
	// A registered policy applies without a node of its own, and the pipeline then waits for
	// analysis before answering.
	cfg = parse(t, "gateways: [{type: litellm}]\n")
	p, err := cfg.policy(env)
	if err != nil || p == nil {
		t.Fatalf("policy %v, %v", p, err)
	}
	pl := pipeline.New(pipeline.Config{Policy: p})
	defer pl.Close()
	if pl.AnswersFirst() {
		t.Error("a build with a policy answers before analyzing")
	}
	if v := pl.Handle(context.Background(), &gateway.Call{Phase: gateway.PhaseRequest, Body: []byte("x")}); v.Action != gateway.Allow {
		t.Errorf("verdict %+v", v)
	}
}
