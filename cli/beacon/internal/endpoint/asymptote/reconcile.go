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
}

// ReconcileResult reports what ReconcileForwarder did.
type ReconcileResult struct {
	VectorConfig string `json:"vector_config,omitempty"`
	PrivacyMode  string `json:"privacy_mode,omitempty"`
	// Skipped names why nothing was checked: the endpoint is not connected, the forwarder is not
	// installed, or the service manager cannot run it here.
	Skipped string `json:"skipped,omitempty"`
	// Unchanged is true when the rendered config already matched the one on disk.
	Unchanged bool `json:"unchanged,omitempty"`
	// Updated is true when a validated config replaced the old one.
	Updated bool `json:"updated,omitempty"`
	// Restarted is true when the forwarder was restarted to load the updated config.
	Restarted bool `json:"restarted,omitempty"`
	// Warning says why a connected forwarder kept its old config, or why an updated one was not
	// restarted. The old config stays in place and keeps running in either case.
	Warning string `json:"warning,omitempty"`
}

// Message is a one-line description of the outcome for install, repair and the package scripts.
func (r ReconcileResult) Message() string {
	switch {
	case r.Warning != "":
		return "Beacon Cloud forwarder: " + r.Warning
	case r.Updated && r.Restarted:
		return "Beacon Cloud forwarder config updated and the forwarder restarted (" + r.VectorConfig + ")."
	case r.Updated:
		return "Beacon Cloud forwarder config updated (" + r.VectorConfig + "); it takes effect when the forwarder next starts."
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
// replaces the old one, and the forwarder is restarted only when the content changed. A config
// Vector rejects is reported as a warning and the running forwarder keeps the old one.
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
	if bytes.Equal(current, []byte(rendered)) {
		result.Unchanged = true
		return result, nil
	}
	// Vector refuses a data_dir that does not exist. Creating a missing one loses nothing: its
	// checkpoints were already gone. An existing one is never cleared here.
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
	// Restart only a forwarder that is running, so reconcile never starts one an operator stopped.
	// Loaded is not enough: on systemd an enabled unit that was stopped still reports Loaded. A
	// forwarder that is not running reads the new config when it is next started.
	if !manager.Status().Running {
		return result, nil
	}
	if err := manager.Load(); err != nil {
		result.Warning = fmt.Sprintf("config updated (%s) but the forwarder could not be restarted: %v", configPath, err)
		return result, nil
	}
	result.Restarted = true
	return result, nil
}
