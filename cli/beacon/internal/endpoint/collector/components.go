package collector

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"
)

// requiredProcessors are the processors ConfigYAML puts in every pipeline that a collector older
// than this CLI does not have. A collector refuses a whole config that names a component it does
// not know, so one of these missing stops capture for every runtime, not just the one it serves.
var requiredProcessors = []string{"claude_api_body"}

// listComponents runs `beacon-otelcol components`, which prints what the binary was built with.
var listComponents = func(ctx context.Context, binary string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, "components").Output()
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
func CheckComponents(binary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := listComponents(ctx, binary)
	if err != nil {
		return nil
	}
	var listed struct {
		Processors []struct {
			Name string `yaml:"name"`
		} `yaml:"processors"`
	}
	if yaml.Unmarshal(out, &listed) != nil || len(listed.Processors) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, processor := range listed.Processors {
		have[processor.Name] = true
	}
	for _, name := range requiredProcessors {
		if !have[name] {
			return fmt.Errorf("collector %s is older than this beacon CLI: it has no %q processor, which the collector config needs; "+
				"use the %s from the same Beacon release, or pass --collector with one", binary, name, BinaryName)
		}
	}
	return nil
}
