package asymptote

import (
	"bytes"
	"fmt"
	"os"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
)

// ReconcileOptions drives ReconcileForwarder.
type ReconcileOptions struct {
	UserMode bool
	// LogPath is the runtime log the forwarder tails. Empty uses config.json's, then the mode's
	// default, which is what connect renders.
	LogPath string
	// Forwarder overrides the service manager; nil uses service.ForwarderManager.
	Forwarder Forwarder
	// Restart restarts a running forwarder even when its config and unit are current. Install,
	// repair and the package upgrade set it, because they may have replaced the runtime log with a
	// copy (switching to restricted log access does), and Vector, which recognizes a file by its
	// first line, keeps reading the replaced file until it restarts. After a restart it resumes in
	// the copy where it left off. A stopped forwarder is still never started.
	Restart bool
}

// ReconcileResult reports what ReconcileForwarder did.
type ReconcileResult struct {
	VectorConfig string `json:"vector_config,omitempty"`
	PrivacyMode  string `json:"privacy_mode,omitempty"`
	// Skipped names why nothing was checked: the endpoint is not connected, the forwarder is not
	// installed, or the service manager cannot run it here.
	Skipped string `json:"skipped,omitempty"`
	// Unchanged is true when both the config and the service log paths are current.
	Unchanged bool `json:"unchanged,omitempty"`
	// Updated is true when a validated config replaced the old one.
	Updated bool `json:"updated,omitempty"`
	// UnitUpdated is true when a stale service unit was rewritten.
	UnitUpdated bool `json:"unit_updated,omitempty"`
	// Restarted is true when the forwarder was restarted, to load an updated config or unit or
	// because the caller asked for a restart.
	Restarted bool `json:"restarted,omitempty"`
	// Warning explains a refused check/update or a failed restart. Updated and UnitUpdated
	// report any changes already written; a warning does not imply those were rolled back.
	Warning string `json:"warning,omitempty"`
}

// Message is a one-line description of the outcome for install, repair and the package scripts.
func (r ReconcileResult) Message() string {
	switch {
	case r.Warning != "":
		return "Beacon Cloud forwarder: " + r.Warning
	case r.UnitUpdated:
		what := "service unit"
		if r.Updated {
			what = "config and service unit"
		}
		if r.Restarted {
			return "Beacon Cloud forwarder " + what + " updated and the forwarder restarted."
		}
		return "Beacon Cloud forwarder " + what + " updated; it takes effect when the forwarder next starts."
	case r.Updated && r.Restarted:
		return "Beacon Cloud forwarder config updated and the forwarder restarted (" + r.VectorConfig + ")."
	case r.Updated:
		return "Beacon Cloud forwarder config updated (" + r.VectorConfig + "); it takes effect when the forwarder next starts."
	case r.Unchanged && r.Restarted:
		return "Beacon Cloud forwarder config is current; the forwarder restarted so it reads the current runtime log."
	case r.Unchanged:
		return "Beacon Cloud forwarder config is current."
	case r.Skipped != "":
		return "Beacon Cloud forwarder not reconciled: " + r.Skipped
	}
	return ""
}

// ReconcileForwarder re-renders a connected endpoint's vector.toml from the pack this binary
// carries, so a pack fix (such as a Metadata-only transform that strips more fields) reaches an
// endpoint that was connected by an older version without a re-connect, which would rotate the
// device key. Install, repair and the package upgrade call it.
//
// It uses only what connect recorded: the enrollment's ingest URL, its privacy mode (changing
// the mode is a connect-time decision, because connect clears the buffer when it does), and the
// endpoint's runtime log. It never enrolls, never reads the device key or rewrites the secrets
// file, makes no network request, and leaves the Vector data directory and its checkpoints
// alone. The new config is validated by Vector exactly as connect validates it before it
// replaces the old one, and a running forwarder is restarted when the config or launchd log paths
// changed, or always when opts.Restart is set. A config Vector rejects is reported as a warning and
// the running forwarder keeps the old one.
//
// The returned error is reserved for a failure that leaves something to fix by hand (the
// validated config could not be written); every reason to leave a working forwarder alone is a
// Skipped or Warning result.
func ReconcileForwarder(opts ReconcileOptions) (ReconcileResult, error) {
	var result ReconcileResult
	// Not connected comes first, before the service manager is asked anything: most endpoints
	// are local-only and this runs on every install.
	if !Connected(opts.UserMode) {
		result.Skipped = "not connected"
		return result, nil
	}
	manager := opts.Forwarder
	if manager == nil {
		manager = service.ForwarderManager{UserMode: opts.UserMode}
	}
	if !manager.Supported() {
		result.Skipped = manager.UnsupportedReason()
		return result, nil
	}
	if unitPath, err := manager.UnitPath(); err != nil || unitPath == "" {
		result.Skipped = "forwarder service is not installed"
		return result, nil
	} else if _, err := os.Stat(unitPath); err != nil {
		result.Skipped = "forwarder service is not installed (" + unitPath + ")"
		return result, nil
	}
	result, err := reconcileFiles(opts, manager)
	if err != nil {
		return result, err
	}
	// A warning means something was kept or is missing (the new config was refused, the Vector or
	// the unit is unusable), and a restart could stop a forwarder that is still running fine, so it
	// never restarts. Otherwise a changed config or unit is loaded, and an unchanged one is restarted
	// only when the caller asked.
	changed := result.Updated || result.UnitUpdated
	if result.Warning != "" || (!changed && !opts.Restart) {
		return result, nil
	}
	// Restart only a forwarder that is running, so reconcile never starts one an operator stopped.
	// Loaded is not enough: on systemd an enabled unit that was stopped still reports Loaded. A
	// forwarder that is not running reads the new config when it is next started.
	if !manager.Status().Running {
		return result, nil
	}
	if err := manager.Load(); err != nil {
		failed := fmt.Sprintf("the forwarder could not be restarted and may be stopped; check `beacon endpoint status`: %v", err)
		if changed {
			failed = fmt.Sprintf("forwarder updated (%s) but it could not be restarted: %v", result.VectorConfig, err)
		}
		if result.Warning != "" {
			failed = result.Warning + "; " + failed
		}
		result.Warning = failed
		return result, nil
	}
	result.Restarted = true
	return result, nil
}

// reconcileFiles is ReconcileForwarder's config and unit half: it brings vector.toml and the
// service unit up to date for an installed forwarder and reports what it did, and never restarts
// anything.
func reconcileFiles(opts ReconcileOptions, manager Forwarder) (ReconcileResult, error) {
	var result ReconcileResult
	configPath := VectorConfigPath(opts.UserMode)
	result.VectorConfig = configPath

	enrollment, err := LoadEnrollment(opts.UserMode)
	if err != nil {
		result.Warning = fmt.Sprintf("kept the existing config: %v", err)
		return result, nil
	}
	if !IsSecureURL(enrollment.IngestURL) {
		result.Warning = "kept the existing config: the enrollment record has no usable ingest URL; run `beacon endpoint connect` to repair it"
		return result, nil
	}
	privacyMode, err := managedprivacy.Normalize(enrollment.PrivacyMode)
	if err != nil {
		result.Warning = fmt.Sprintf("kept the existing config: the enrollment record's privacy mode is not valid: %v", err)
		return result, nil
	}
	cfg, cfgErr := endpointconfig.Load(opts.UserMode)
	if cfgErr == nil && cfg.ManagedIngest != nil && cfg.ManagedIngest.PrivacyMode != "" {
		recorded, err := managedprivacy.Normalize(cfg.ManagedIngest.PrivacyMode)
		if err != nil || recorded != privacyMode {
			// Two records that disagree are not ours to settle: the mode decides what leaves the
			// machine, and choosing it is connect's job.
			result.Warning = fmt.Sprintf("kept the existing config: config.json records privacy mode %q but the enrollment records %q; run `beacon endpoint connect --privacy-mode <mode>` to choose one", cfg.ManagedIngest.PrivacyMode, enrollment.PrivacyMode)
			return result, nil
		}
	}
	result.PrivacyMode = privacyMode
	logPath := opts.LogPath
	if logPath == "" && cfgErr == nil {
		logPath = cfg.LogPath
	}
	if logPath == "" {
		logPath = endpointconfig.Default(opts.UserMode, "").LogPath
	}

	// Validate with the Vector the service runs. A recorded binary that has gone means the
	// forwarder is already broken, and a different Vector proving the config would prove nothing.
	if enrollment.VectorBin != "" {
		if _, err := os.Stat(enrollment.VectorBin); err != nil {
			result.Warning = fmt.Sprintf("kept the existing config: the forwarder's Vector %s is missing; reinstall Beacon or run `beacon endpoint connect`", enrollment.VectorBin)
			return result, nil
		}
	}
	vector, err := FindVector(enrollment.VectorBin)
	if err != nil {
		result.Warning = fmt.Sprintf("kept the existing config: %v", err)
		return result, nil
	}

	dataDir := DataDir(opts.UserMode)
	rendered, err := RenderVectorConfig(RenderOptions{
		LogPath:     logPath,
		IngestURL:   enrollment.IngestURL,
		SecretsFile: SecretsPath(opts.UserMode),
		DataDir:     dataDir,
		PrivacyMode: privacyMode,
	})
	if err != nil {
		result.Warning = fmt.Sprintf("kept the existing config: %v", err)
		return result, nil
	}
	current, err := os.ReadFile(configPath)
	if err != nil {
		result.Warning = fmt.Sprintf("kept the existing config: %v", err)
		return result, nil
	}
	configCurrent := bytes.Equal(current, []byte(rendered))
	unitCurrent, err := manager.UnitCurrent(vector.Path, configPath)
	if err != nil {
		result.Warning = fmt.Sprintf("kept the existing config and service unit: could not read the forwarder service unit: %v", err)
		return result, nil
	}
	if configCurrent && unitCurrent {
		result.Unchanged = true
		// A restart loads this config into the Vector the service runs now, which an upgrade may
		// have replaced, so it must still accept it. If not, the running forwarder is left alone.
		if opts.Restart {
			if err := ValidateVectorConfig(vector.Path, configPath); err != nil {
				result.Warning = fmt.Sprintf("left the forwarder running without a restart because its Vector rejects the current config: %v", err)
			}
		}
		return result, nil
	}
	if !configCurrent {
		// Creating a missing data directory loses no existing checkpoints.
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			result.Warning = fmt.Sprintf("kept the existing config: %v", err)
			return result, nil
		}
		if err := preValidateVectorConfig(vector.Path, configPath, []byte(rendered)); err != nil {
			result.Warning = fmt.Sprintf("kept the existing config because Vector rejected the new one: %v", err)
			return result, nil
		}
		if err := atomicfile.Write(configPath, []byte(rendered), 0o644); err != nil {
			return result, fmt.Errorf("could not write the validated forwarder config %s: %w", configPath, err)
		}
		result.Updated = true
	}
	if !unitCurrent {
		if _, err := manager.WriteUnit(vector.Path, configPath); err != nil {
			result.Warning = fmt.Sprintf("could not update the forwarder service unit: %v", err)
			if result.Updated {
				result.Warning = fmt.Sprintf("config updated (%s) but the forwarder service unit could not be updated: %v", configPath, err)
			}
			return result, nil
		}
		result.UnitUpdated = true
	}
	return result, nil
}
