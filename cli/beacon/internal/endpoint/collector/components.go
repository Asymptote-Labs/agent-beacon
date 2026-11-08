package collector

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"
)

// requiredComponents are the components ConfigYAML always writes that a collector older than this
// CLI does not have. A collector refuses a whole config that names a component it does not know,
// so one of these missing stops capture for every runtime, not just the one it serves.
var requiredComponents = map[string][]string{
	"processor": {"claude_api_body", "beacon_privacy"},
	"extension": {"loopback_host"},
}

// listComponents runs `beacon-otelcol components`, which prints what the binary was built with.
var listComponents = func(ctx context.Context, binary string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, "components")
	cmd.WaitDelay = componentsWaitDelay
	return cmd.Output()
}

type namedComponents []struct {
	Name string `yaml:"name"`
}

// componentListing is what `beacon-otelcol components` prints, by kind.
type componentListing struct {
	Receivers  namedComponents `yaml:"receivers"`
	Processors namedComponents `yaml:"processors"`
	Extensions namedComponents `yaml:"extensions"`
	Exporters  namedComponents `yaml:"exporters"`
}

func (l componentListing) has(kind, name string) bool {
	var list namedComponents
	switch kind {
	case "receiver":
		list = l.Receivers
	case "processor":
		list = l.Processors
	case "extension":
		list = l.Extensions
	case "exporter":
		list = l.Exporters
	}
	for _, c := range list {
		if c.Name == name {
			return true
		}
	}
	return false
}

// readComponents lists binary's components. ok is false for a binary that cannot list them -- a
// stand-in, or a build without the command -- or whose listing does not parse: nothing to judge it on.
func readComponents(binary string) (componentListing, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), componentsTimeout)
	defer cancel()
	out, err := listComponents(ctx, binary)
	if err != nil {
		return componentListing{}, false
	}
	var listed componentListing
	if yaml.Unmarshal(out, &listed) != nil || len(listed.Processors) == 0 {
		return componentListing{}, false
	}
	return listed, true
}

// CheckComponents refuses a collector binary that is older than the config this CLI writes.
//
// Packages and archives ship the collector beside the CLI from the same release, so this fails only
// when something else is picked up: an older beacon-otelcol earlier on PATH, or one passed with
// --collector. Without it, install writes the config, the collector rejects it at start, and
// nothing is captured until someone reads the collector's own log.
//
// A binary that cannot list its components -- a stand-in, or a build that does not support the
// command -- is not refused: there is nothing to judge it on, and it fails at start as it would
// have before.
//
// extensions names the extensions the config will use beyond health_check -- beacon_ingest, in the
// restricted log access mode -- which are refused the same way.
func CheckComponents(binary string, extensions ...string) error {
	return CheckComponentsFor(binary, extensions, nil, nil)
}

// CheckComponentsFor is CheckComponents with the processors and exporters the config will use
// beyond Beacon's own -- the OTLP forward's -- refused the same way. Where ConfigYAML renders peer
// attribution, its receiver and processor are required too.
//
// Which config that is depends on the collector: on macOS and Windows peer attribution is rendered
// only for a collector that lists PeerLookupExtension (see PeerAttributionFor), so its receiver and
// processor are required exactly when the listing says the config will name them.
func CheckComponentsFor(binary string, extensions, processors, exporters []string) error {
	listed, ok := readComponents(binary)
	if !ok {
		return nil
	}
	var receivers []string
	if PeerAttribution && (!peerLookupMarkerRequired || listed.has("extension", PeerLookupExtension)) {
		processors = append(append([]string{}, processors...), PeerProcessor)
		receivers = append(receivers, OTLPHTTPReceiver)
	}
	have := map[string]bool{}
	for _, c := range listed.Processors {
		have["processor/"+c.Name] = true
	}
	for _, c := range listed.Extensions {
		have["extension/"+c.Name] = true
	}
	want := map[string][]string{
		"processor": append(append([]string{}, requiredComponents["processor"]...), processors...),
		"extension": requiredComponents["extension"],
	}
	for _, kind := range []string{"processor", "extension"} {
		for _, name := range want[kind] {
			if !have[kind+"/"+name] {
				return fmt.Errorf("collector %s is older than this beacon CLI: it has no %q %s, which the collector config needs; "+
					"use the %s from the same Beacon release, or pass --collector with one", binary, name, kind, BinaryName)
			}
		}
	}
	haveReceiver := map[string]bool{}
	for _, receiver := range listed.Receivers {
		haveReceiver[receiver.Name] = true
	}
	for _, name := range receivers {
		if !haveReceiver[name] {
			return fmt.Errorf("collector %s is older than this beacon CLI: it has no %q receiver, which the collector config needs; "+
				"use the %s from the same Beacon release, or pass --collector with one", binary, name, BinaryName)
		}
	}
	haveExtension := map[string]bool{}
	for _, extension := range listed.Extensions {
		haveExtension[extension.Name] = true
	}
	for _, name := range extensions {
		if !haveExtension[name] {
			return fmt.Errorf("collector %s is older than this beacon CLI: it has no %q extension, which the restricted log access mode needs; "+
				"use the %s from the same Beacon release, or pass --collector with one", binary, name, BinaryName)
		}
	}
	haveExporter := map[string]bool{}
	for _, exporter := range listed.Exporters {
		haveExporter[exporter.Name] = true
	}
	for _, name := range exporters {
		if !haveExporter[name] {
			return fmt.Errorf("collector %s is older than this beacon CLI: it has no %q exporter, which OTLP forwarding needs; "+
				"use the %s from the same Beacon release, or pass --collector with one", binary, name, BinaryName)
		}
	}
	return nil
}

// componentsTimeout bounds `beacon-otelcol components`. A variable for tests.
var componentsTimeout = 10 * time.Second

// componentsWaitDelay is how long listComponents waits, after the timeout kills the binary, for its
// output pipes to close. A child the binary started keeps them open after the binary is killed (a
// shell script's commands are such children), and without a bound Output() would wait for that child
// however long it runs, so a binary that hangs on `components` would stall install, repair and every
// config render on macOS and Windows, which consult this listing to decide on peer attribution.
const componentsWaitDelay = 2 * time.Second
