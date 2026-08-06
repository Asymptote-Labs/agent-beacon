package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/credentials"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/image"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
)

// checkStatus is deliberately three-valued. "warn" exists for things that are not broken but
// will produce a misleading result if ignored -- notably an exporter change that a downloaded
// collector does not contain.
type checkStatus string

const (
	statusOK   checkStatus = "ok"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
)

type doctorCheck struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
	// Fix is the exact command or action that resolves this, so neither a human nor an agent
	// has to guess.
	Fix string `json:"fix,omitempty"`
}

type doctorResult struct {
	Checks []doctorCheck `json:"checks"`
	Ready  bool          `json:"ready"`
}

// cmdDoctor verifies every prerequisite and says exactly how to fix what is missing.
//
// This is the first thing to run, and the first step in the documented workflow, because the
// alternative is a confusing mid-run failure: a missing collector surfaces as an image-staging
// error, and a missing credential as an authentication failure inside the sandbox.
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print results as JSON for programmatic use")
	fix := fs.Bool("fix", false, "resolve what can be resolved automatically (downloads the collector)")
	modalSecret := fs.String("modal-secret", "", "name of a Modal secret holding ANTHROPIC_API_KEY")
	keyCommand := fs.String("api-key-command", "", "command whose stdout is the Anthropic API key")
	provider := fs.String("provider", "modal", "check the prerequisites for this provider: modal, github, or local")
	repo := fs.String("repo", "", "with --provider github, the owner/name that will be dispatched to")
	fs.Parse(args)

	root, err := repoRoot()
	if err != nil {
		return err
	}

	var res doctorResult
	add := func(c doctorCheck) { res.Checks = append(res.Checks, c) }

	// --- toolchain ---
	if out, err := exec.Command("go", "version").Output(); err == nil {
		add(doctorCheck{Name: "go", Status: statusOK, Detail: strings.TrimSpace(string(out))})
	} else {
		add(doctorCheck{Name: "go", Status: statusFail,
			Detail: "go not found on PATH",
			Fix:    "install Go 1.24+ from https://go.dev/dl/"})
	}

	// The checks are per-provider, because a check that cannot apply must not be reported as a
	// failure. Running the Modal checks before a dispatched Windows run would demand a Modal
	// account and a Linux Beacon build for a run that uses neither, and `ready: false` on
	// prerequisites the chosen provider does not have would train people to ignore the field.
	switch *provider {
	case "github":
		for _, c := range githubChecks(root, *repo) {
			add(c)
		}
	case "local":
		for _, c := range localChecks() {
			add(c)
		}
	default:
		// --- sandbox provider auth ---
		// Attempting a real connection is the only honest check: credentials can be present but
		// expired, and discovering that mid-run wastes a session.
		ctx := context.Background()
		if prov, err := sandbox.NewModal(ctx, appName); err == nil {
			prov.Close()
			add(doctorCheck{Name: "modal_auth", Status: statusOK, Detail: "authenticated"})
		} else {
			add(doctorCheck{Name: "modal_auth", Status: statusFail,
				Detail: firstLine(err.Error()),
				Fix:    "pip install modal && modal token new    (or set MODAL_TOKEN_ID and MODAL_TOKEN_SECRET)"})
		}

		// --- agent credential ---
		creds, credErr := credentials.Resolve(credentials.Options{
			ProviderSecretName: *modalSecret,
			KeyCommand:         *keyCommand,
		})
		add(credentialCheck(creds, credErr, *keyCommand))

		// --- Beacon binary under test ---
		beaconPath := image.BeaconPath(root)
		if fi, err := os.Stat(beaconPath); err == nil && fi.Size() > 0 {
			add(doctorCheck{Name: "beacon_binary", Status: statusOK, Detail: beaconPath})
		} else {
			add(doctorCheck{Name: "beacon_binary", Status: statusFail,
				Detail: "missing " + beaconPath,
				Fix:    image.BuildBeaconHint})
		}

		// --- collector ---
		collectorPath := image.CollectorPath(root)
		if fi, err := os.Stat(collectorPath); err == nil && fi.Size() > 0 {
			add(doctorCheck{Name: "collector_binary", Status: statusOK, Detail: collectorPath})
		} else if *fix {
			if p, err := image.EnsureCollector(root, func(f string, a ...any) {
				if !*asJSON {
					fmt.Printf("  "+f+"\n", a...)
				}
			}); err == nil {
				add(doctorCheck{Name: "collector_binary", Status: statusOK, Detail: "downloaded to " + p})
			} else {
				add(doctorCheck{Name: "collector_binary", Status: statusFail, Detail: firstLine(err.Error()),
					Fix: "see the build instructions in the error above"})
			}
		} else {
			add(doctorCheck{Name: "collector_binary", Status: statusFail,
				Detail: "missing " + collectorPath,
				Fix:    "beacon-sandbox doctor --fix    (downloads it from the latest release)"})
		}

		// --- the trap: local exporter changes a downloaded collector will not contain ---
		stale, reason := image.CollectorIsStale(root)
		add(freshnessCheck(stale, reason))
	}

	res.Ready = true
	for _, c := range res.Checks {
		if c.Status == statusFail {
			res.Ready = false
		}
	}

	if *asJSON {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
	} else {
		printDoctor(res)
	}
	if !res.Ready {
		return fmt.Errorf("prerequisites are not met; fix the items above")
	}
	return nil
}

// credentialCheck reports on the Anthropic credential.
//
// The distinction it draws matters more than it looks. An explicitly configured helper that
// fails is not the same problem as no credential at all, but the first version of this
// collapsed both into "not configured" and printed the env-var fix -- which sends someone
// whose `op read` is broken off to set an environment variable instead of unlocking their
// vault. Resolve already produces a precise error; the job here is to not throw it away.
func credentialCheck(creds credentials.Resolved, err error, keyCommand string) doctorCheck {
	if err != nil {
		if strings.TrimSpace(keyCommand) != "" {
			return doctorCheck{Name: "anthropic_credential", Status: statusFail,
				Detail: firstLine(err.Error()),
				Fix:    "fix the helper so it prints the key: " + keyCommand}
		}
		return doctorCheck{Name: "anthropic_credential", Status: statusFail,
			Detail: "not configured",
			Fix:    "export ANTHROPIC_API_KEY=sk-ant-…    (or --modal-secret NAME, or --api-key-command CMD)"}
	}

	c := doctorCheck{Name: "anthropic_credential", Status: statusOK, Detail: creds.Describe()}
	if !creds.LeakCheckPossible() {
		// Not a failure -- it is the most secure option. But the artifact leak check cannot
		// run, and that must be visible rather than silently skipped.
		c.Status = statusWarn
		c.Fix = "use --api-key-command or ANTHROPIC_API_KEY if you want the artifact leak check to run"
	}
	return c
}

// freshnessCheck reports whether the collector on disk matches the exporter sources.
//
// CollectorIsStale returns a complete reason -- uncommitted changes, drift from the release a
// binary was downloaded from, or sources newer than a local build. A first version prefixed every
// result with the uncommitted wording, which doubled that case and mislabelled the other two, so
// the check that exists to stop you verifying the wrong binary pointed at the wrong cause.
// Reported by Cursor Bugbot after the reason string grew from one case to three.
func freshnessCheck(stale bool, reason string) doctorCheck {
	if !stale {
		return doctorCheck{Name: "collector_freshness", Status: statusOK,
			Detail: "collector matches the exporter sources in this tree"}
	}
	return doctorCheck{Name: "collector_freshness", Status: statusWarn,
		Detail: reason,
		Fix: "the telemetry normalization compiles into beacon-otelcol, not the beacon CLI, " +
			"so rebuild the collector or your run will verify the wrong binary:\n" +
			"      go install go.opentelemetry.io/collector/cmd/builder@v0.121.0\n" +
			"      cd collector-builder && mkdir -p dist && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \"$(go env GOPATH)/bin/builder\" --config builder.yaml\n" +
			"      cp dist/beacon-otelcol/beacon-otelcol dist/beacon-otelcol/linux_amd64/beacon-otelcol"}
}

func printDoctor(res doctorResult) {
	symbol := map[checkStatus]string{statusOK: "ok  ", statusWarn: "warn", statusFail: "FAIL"}
	for _, c := range res.Checks {
		fmt.Printf("%s  %-22s %s\n", symbol[c.Status], c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Printf("      fix: %s\n", c.Fix)
		}
	}
	fmt.Println()
	if res.Ready {
		fmt.Printf("ready. next: go run ./cmd/beacon-sandbox run --scenario s02-bash-command\n")
		return
	}
	fmt.Printf("not ready — resolve the FAIL items above, then rerun doctor\n")
	if runtime.GOOS == "windows" {
		fmt.Println("note: the sandbox itself runs Linux, but these commands assume a POSIX shell")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
