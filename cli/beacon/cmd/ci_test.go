package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The GitHub Action (action.yml at the repository root) is how CI users run Beacon, and its exec
// and finish steps turn the `upload` input into repeated --upload flags. Nothing else runs those
// composite steps, so a step that drops the input -- or hands the CLI a flag it no longer has --
// ships unnoticed and breaks every workflow using it. Each step's script runs here against a stub
// beacon, and what it passed is parsed by the real command's flags.
func TestCIActionPassesUploadsToTheCommandsItRuns(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("parse action.yml: %v", err)
	}
	scripts := map[string]string{}
	for _, step := range action.Runs.Steps {
		scripts[step.ID] = step.Run
	}

	for step, command := range map[string]*cobra.Command{"exec": ciExecCmd, "finish": ciFinishCmd} {
		t.Run(step, func(t *testing.T) {
			script := scripts[step]
			if script == "" {
				t.Fatalf("action.yml has no %q step", step)
			}
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args")
			stub := filepath.Join(dir, "beacon")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$ARGS_FILE\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			run := exec.Command("bash", "-c", script)
			run.Env = append(os.Environ(),
				"BEACON_BIN="+stub, "ARGS_FILE="+argsFile,
				"INPUT_UPLOAD= s3, ,gcs ", "INPUT_COMMAND=true", "INPUT_MIN_EVENTS=1", "INPUT_REQUIRE_TELEMETRY=true",
				"INPUT_HARNESSES=", "INPUT_FORWARD=", "INPUT_FORWARD_ENDPOINT=",
				"LOG_PATH="+filepath.Join(dir, "runtime.jsonl"), "STATE_PATH="+filepath.Join(dir, "session.json"))
			if out, err := run.CombinedOutput(); err != nil {
				t.Fatalf("the %s step failed: %v\n%s", step, err, out)
			}
			recorded, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("the %s step never ran beacon: %v", step, err)
			}
			args := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
			if len(args) < 2 || args[0] != "ci" || args[1] != step {
				t.Fatalf("the %s step ran beacon %q, want beacon ci %s", step, args, step)
			}

			prev := ciOpts
			flags := pflag.NewFlagSet(step, pflag.ContinueOnError)
			flags.AddFlagSet(command.Flags())
			flags.AddFlagSet(command.InheritedFlags())
			t.Cleanup(func() {
				ciOpts = prev
				flags.VisitAll(func(f *pflag.Flag) { f.Changed = false })
			})
			if err := flags.Parse(args[2:]); err != nil {
				t.Fatalf("beacon ci %s rejects what the action passes it: %v\nargs: %q", step, err, args)
			}
			if want := []string{"s3", "gcs"}; !reflect.DeepEqual(ciOpts.uploads, want) {
				t.Fatalf("upload input \" s3, ,gcs \" reached beacon ci %s as %q, want %q", step, ciOpts.uploads, want)
			}
		})
	}
}
