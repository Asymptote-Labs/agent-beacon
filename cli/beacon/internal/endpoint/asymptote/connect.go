package asymptote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
)

// Forwarder is the service-manager surface Connect and Disconnect need. service.ForwarderManager
// is the production implementation; tests supply a fake so no launchctl or systemctl runs.
type Forwarder interface {
	Supported() bool
	UnsupportedReason() string
	Label() string
	UnitPath() (string, error)
	WriteUnit(vectorBin, configPath string) (string, error)
	Load() error
	Unload() error
	RemoveUnits()
	Status() service.Status
}

// ConnectOptions drives Connect.
type ConnectOptions struct {
	UserMode bool
	// LogPath is the runtime log Vector tails; the inventory log is derived from it.
	LogPath string
	// VectorBin overrides Vector discovery.
	VectorBin string
	// Forwarder overrides the service manager; nil uses service.ForwarderManager.
	Forwarder Forwarder
	// Enroll is the browser flow; the CLI fills Device, DashboardURL, browser opener.
	Enroll EnrollOptions
	// AccountEnroll replaces the browser approval with the signed-in CLI account.
	// Connect fills Device after pinning the install id.
	AccountEnroll *AccountEnrollOptions
	// InstallID identifies this machine across re-enrollments. Empty reuses the stored
	// enrollment's id or mints a new one.
	InstallID string
	// PrivacyMode controls the Vector-side transform before managed upload.
	PrivacyMode string
	Out         io.Writer
	// Now is injectable for tests.
	Now func() time.Time
}

// ConnectResult reports what Connect wrote and where telemetry now goes.
type ConnectResult struct {
	Enrollment     Enrollment     `json:"enrollment"`
	VectorConfig   string         `json:"vector_config"`
	SecretsFile    string         `json:"secrets_file"`
	UnitPath       string         `json:"unit_path"`
	Forwarder      string         `json:"forwarder"`
	ReEnrolled     bool           `json:"re_enrolled"`
	ForwarderState service.Status `json:"forwarder_state"`
}

// Connect enrolls this machine and starts the forwarder.
//
// Failure ordering is deliberate: Vector is located before the browser opens, so a machine
// without Vector never mints a key it cannot use; the key is written to the 0600 secrets file
// before the config that references it; the config is validated before a service unit points at
// it; and the enrollment record is saved last, so status never claims a connection that did not
// finish. A re-run on an enrolled machine reuses its install id, which rotates the key in place
// on the server rather than creating a second device.
func Connect(ctx context.Context, opts ConnectOptions) (*ConnectResult, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	privacyMode, err := managedprivacy.Normalize(opts.PrivacyMode)
	if err != nil {
		return nil, err
	}
	manager := opts.Forwarder
	if manager == nil {
		manager = service.ForwarderManager{UserMode: opts.UserMode}
	}
	if !manager.Supported() {
		return nil, errors.New(manager.UnsupportedReason())
	}
	vector, err := FindVector(opts.VectorBin)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Using Vector %s at %s\n", vector.Version, vector.Path)

	previous, err := LoadEnrollment(opts.UserMode)
	if err != nil && !errors.Is(err, ErrNotEnrolled) {
		return nil, err
	}
	installID := opts.InstallID
	if installID == "" && previous != nil {
		installID = previous.InstallID
	}
	if installID == "" {
		installID = ReadInstallID(opts.UserMode)
	}
	if installID == "" {
		installID, err = newInstallID()
		if err != nil {
			return nil, err
		}
	}
	// Pin the id before the dashboard learns it, so a failure after approval retries as
	// the same device.
	if err := WriteInstallID(opts.UserMode, installID); err != nil {
		return nil, err
	}
	enrollOpts := opts.Enroll
	enrollOpts.Device.InstallID = installID
	if enrollOpts.Device.InstallMode == "" {
		enrollOpts.Device.InstallMode = installMode(opts.UserMode)
	}
	if enrollOpts.Out == nil {
		enrollOpts.Out = out
	}

	var result *EnrollResult
	if opts.AccountEnroll != nil {
		accountEnroll := *opts.AccountEnroll
		accountEnroll.Device = enrollOpts.Device
		result, err = EnrollAccount(ctx, accountEnroll)
	} else {
		result, err = Enroll(ctx, enrollOpts)
	}
	if err != nil {
		return nil, err
	}
	if opts.AccountEnroll != nil {
		fmt.Fprintf(out, "Account authorized device enrollment for %s (device %s, key %s)\n", displayOrg(result), result.DeviceID, result.KeyPrefix)
	} else {
		fmt.Fprintf(out, "Approved for %s (device %s, key %s)\n", displayOrg(result), result.DeviceID, result.KeyPrefix)
	}

	// Secrets first, then the config that references them, then the unit that runs it.
	if err := WriteSecrets(opts.UserMode, result.DeviceKey); err != nil {
		return nil, fmt.Errorf("could not store the device key: %w", err)
	}
	result.DeviceKey = ""
	dataDir := DataDir(opts.UserMode)
	logPath := opts.LogPath
	if logPath == "" {
		logPath = endpointconfig.Default(opts.UserMode, "").LogPath
	}

	// Render and pre-validate before touching the service manager. A privacy-mode
	// change unloads the forwarder below; validating first ensures a bad config
	// (unsupported VRL, permissions, etc.) never stops a working forwarder.
	rendered, err := RenderVectorConfig(RenderOptions{
		LogPath:     logPath,
		IngestURL:   result.IngestURL,
		SecretsFile: SecretsPath(opts.UserMode),
		DataDir:     dataDir,
		PrivacyMode: privacyMode,
	})
	if err != nil {
		return nil, err
	}
	configPath := VectorConfigPath(opts.UserMode)
	if err := preValidateVectorConfig(vector.Path, configPath, []byte(rendered)); err != nil {
		return nil, err
	}

	if previous != nil {
		previousMode, _ := managedprivacy.Normalize(previous.PrivacyMode)
		if previousMode != privacyMode {
			// Stop the forwarder before clearing its buffer so a running Vector
			// cannot keep sending or recreate old-mode buffer files.
			_ = manager.Unload()
			if err := os.RemoveAll(dataDir); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("could not clear the buffer after a privacy mode change: %w", err)
			}
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(configPath, []byte(rendered), 0o644); err != nil {
		return nil, err
	}
	unitPath, err := manager.WriteUnit(vector.Path, configPath)
	if err != nil {
		return nil, err
	}
	if err := manager.Load(); err != nil {
		return nil, fmt.Errorf("forwarder installed at %s but could not be started: %w", unitPath, err)
	}

	dashboardURL := auth.ResolveDashboardURL(enrollOpts.DashboardURL)
	if opts.AccountEnroll != nil {
		dashboardURL = strings.TrimRight(opts.AccountEnroll.BaseURL, "/")
	}
	enrollment := Enrollment{
		InstallID:        installID,
		IngestURL:        result.IngestURL,
		DashboardURL:     dashboardURL,
		DeviceID:         result.DeviceID,
		KeyPrefix:        result.KeyPrefix,
		OrganizationID:   result.OrganizationID,
		OrganizationName: result.OrganizationName,
		Email:            result.Email,
		EnrolledAt:       now().UTC(),
		VectorBin:        vector.Path,
		VectorVersion:    vector.Version,
		PrivacyMode:      privacyMode,
	}
	if result.ExpiresAt != nil {
		enrollment.ExpiresAt = *result.ExpiresAt
	}
	if err := SaveEnrollment(opts.UserMode, enrollment); err != nil {
		return nil, err
	}
	if err := updateConfig(opts.UserMode, logPath, &endpointconfig.ManagedIngest{
		Enabled:        true,
		IngestURL:      result.IngestURL,
		DeviceID:       result.DeviceID,
		OrganizationID: result.OrganizationID,
		PrivacyMode:    privacyMode,
	}); err != nil {
		return nil, err
	}
	return &ConnectResult{
		Enrollment:     enrollment,
		VectorConfig:   configPath,
		SecretsFile:    SecretsPath(opts.UserMode),
		UnitPath:       unitPath,
		Forwarder:      manager.Label(),
		ReEnrolled:     previous != nil,
		ForwarderState: manager.Status(),
	}, nil
}

// DisconnectOptions drives Disconnect.
type DisconnectOptions struct {
	UserMode bool
	LogPath  string
	// Forwarder overrides the service manager; nil uses service.ForwarderManager.
	Forwarder Forwarder
	// KeepCredentials leaves enrollment.json and the secrets file in place.
	KeepCredentials bool
}

// Disconnect stops and removes the forwarder and, unless asked to keep them, the local
// credentials. Nothing is revoked server-side: that is done from the dashboard's Beacon
// Endpoints page, and the CLI says so.
func Disconnect(opts DisconnectOptions) error {
	manager := opts.Forwarder
	if manager == nil {
		manager = service.ForwarderManager{UserMode: opts.UserMode}
	}
	var problems []error
	// Only touch the service manager when a forwarder was actually installed here. Uninstall
	// calls Disconnect unconditionally, and a bootout against the live launchd domain from
	// an endpoint that was never connected would stop a forwarder belonging to a different
	// install (or to the developer running the test suite). A directory holding only the
	// pinned install id from a cancelled connect is not a forwarder.
	if forwarderInstalled(opts.UserMode, manager) {
		if err := manager.Unload(); err != nil {
			problems = append(problems, fmt.Errorf("stop forwarder: %w", err))
		}
		manager.RemoveUnits()
	}
	if err := RemoveState(opts.UserMode, opts.KeepCredentials); err != nil {
		problems = append(problems, fmt.Errorf("remove %s: %w", Dir(opts.UserMode), err))
	}
	if err := updateConfig(opts.UserMode, opts.LogPath, nil); err != nil && !os.IsNotExist(err) {
		problems = append(problems, fmt.Errorf("update endpoint config: %w", err))
	}
	return errors.Join(problems...)
}

// forwarderInstalled reports whether Connect got as far as installing a forwarder: the
// rendered config, an enrollment record, or the service unit itself exists.
func forwarderInstalled(userMode bool, manager Forwarder) bool {
	if Connected(userMode) {
		return true
	}
	// Either file alone is still state to clean up: a kept enrollment record, or the config
	// a connect wrote before failing at validate or load.
	for _, path := range []string{EnrollmentPath(userMode), VectorConfigPath(userMode)} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	if path, err := manager.UnitPath(); err == nil && path != "" {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// updateConfig records (or clears) the non-secret managed_ingest block in config.json. A
// missing config is created with defaults so a hook-only install can still connect.
func updateConfig(userMode bool, logPath string, managed *endpointconfig.ManagedIngest) error {
	cfg, err := endpointconfig.Load(userMode)
	if err != nil {
		if managed == nil {
			// Clearing the block from a missing or unreadable config is nothing to do;
			// uninstall calls this right before removing the file anyway.
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		cfg = endpointconfig.Default(userMode, logPath)
	}
	if managed == nil {
		if cfg.ManagedIngest == nil {
			return nil
		}
		cfg.ManagedIngest = nil
	} else {
		cfg.ManagedIngest = managed
	}
	_, err = endpointconfig.Save(cfg)
	return err
}

func installMode(userMode bool) string {
	if userMode {
		return "user"
	}
	return "system"
}

func displayOrg(r *EnrollResult) string {
	if r.OrganizationName != "" {
		return r.OrganizationName
	}
	return r.OrganizationID
}

// newInstallID mints a random 128-bit identifier for this machine.
func newInstallID() (string, error) {
	var buf [16]byte
	if _, err := randRead(buf[:]); err != nil {
		return "", fmt.Errorf("could not generate an install id: %w", err)
	}
	return fmt.Sprintf("%x", buf), nil
}

// ForwarderStatus reports the service state of the forwarder for the selected mode.
func ForwarderStatus(userMode bool) service.Status {
	return service.ForwarderManager{UserMode: userMode}.Status()
}

// BufferBytes is the size of Vector's on-disk buffer and checkpoints.
func BufferBytes(userMode bool) int64 {
	return dirSize(filepath.Join(DataDir(userMode)))
}
