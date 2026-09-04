package asymptote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Files under <BaseDir>/asymptote/. The directory is 0700 and the two credential-adjacent
// files are 0600; vector.toml is 0644 because it holds no secret, only the SECRET[] reference.
const (
	DirName            = "asymptote"
	EnrollmentFileName = "enrollment.json"
	// InstallIDFileName pins this machine's install id before enrollment starts, so a
	// connect that fails after approval (validate, unit, load) retries with the same id and
	// the server rotates the device's key instead of registering a second device.
	InstallIDFileName = "install-id"
	SecretsFileName   = "vector-secrets.json"
	VectorConfigName  = "vector.toml"
	DataDirName       = "vector-data"
)

// Dir is the managed-ingest state directory for the selected endpoint mode.
func Dir(userMode bool) string { return filepath.Join(endpointconfig.BaseDir(userMode), DirName) }

// EnrollmentPath, SecretsPath, VectorConfigPath and DataDir locate the individual files.
func EnrollmentPath(userMode bool) string   { return filepath.Join(Dir(userMode), EnrollmentFileName) }
func SecretsPath(userMode bool) string      { return filepath.Join(Dir(userMode), SecretsFileName) }
func VectorConfigPath(userMode bool) string { return filepath.Join(Dir(userMode), VectorConfigName) }
func DataDir(userMode bool) string          { return filepath.Join(Dir(userMode), DataDirName) }
func InstallIDPath(userMode bool) string    { return filepath.Join(Dir(userMode), InstallIDFileName) }

// ReadInstallID returns the pinned install id, or "" when none has been written.
func ReadInstallID(userMode bool) string {
	data, err := os.ReadFile(InstallIDPath(userMode))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteInstallID pins the install id for this machine.
func WriteInstallID(userMode bool, installID string) error {
	if err := ensureDir(userMode); err != nil {
		return err
	}
	return writeFileAtomic(InstallIDPath(userMode), []byte(installID+"\n"), 0o600)
}

// Connected reports whether this endpoint is enrolled and has its forwarder configured: both
// enrollment.json and vector.toml exist. Either alone is not a connection. disconnect
// --keep-credentials leaves the record behind without a config, and a connect that failed at
// vector validate or at loading the service leaves the config behind without a record; a
// machine in either state must be offered connect again and reported as not connected.
func Connected(userMode bool) bool {
	if _, err := os.Stat(VectorConfigPath(userMode)); err != nil {
		return false
	}
	_, err := os.Stat(EnrollmentPath(userMode))
	return err == nil
}

// Enrollment is the non-secret record of a device's enrollment. Everything `beacon endpoint
// status` prints comes from here; the device key itself is only in the secrets file.
type Enrollment struct {
	InstallID        string    `json:"install_id"`
	IngestURL        string    `json:"ingest_url"`
	DashboardURL     string    `json:"dashboard_url"`
	DeviceID         string    `json:"device_id"`
	KeyPrefix        string    `json:"key_prefix"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name,omitempty"`
	Email            string    `json:"email,omitempty"`
	EnrolledAt       time.Time `json:"enrolled_at"`
	ExpiresAt        string    `json:"expires_at,omitempty"`
	VectorBin        string    `json:"vector_bin,omitempty"`
	VectorVersion    string    `json:"vector_version,omitempty"`
}

// ErrNotEnrolled is returned when no enrollment record exists.
var ErrNotEnrolled = errors.New("this endpoint is not connected to Asymptote managed ingest")

// LoadEnrollment reads the enrollment record.
func LoadEnrollment(userMode bool) (*Enrollment, error) {
	data, err := os.ReadFile(EnrollmentPath(userMode))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotEnrolled
		}
		return nil, err
	}
	var enrollment Enrollment
	if err := json.Unmarshal(data, &enrollment); err != nil {
		return nil, fmt.Errorf("enrollment record %s is not valid JSON: %w", EnrollmentPath(userMode), err)
	}
	return &enrollment, nil
}

// SaveEnrollment writes the record with 0600 permissions inside a 0700 directory.
func SaveEnrollment(userMode bool, enrollment Enrollment) error {
	if err := ensureDir(userMode); err != nil {
		return err
	}
	data, err := json.MarshalIndent(enrollment, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(EnrollmentPath(userMode), append(data, '\n'), 0o600)
}

// WriteSecrets stores the device key in the file Vector's secret backend reads.
func WriteSecrets(userMode bool, deviceKey string) error {
	if !strings.HasPrefix(deviceKey, "bcn_device_") {
		return errors.New("refusing to store a credential that is not a Beacon device key")
	}
	if err := ensureDir(userMode); err != nil {
		return err
	}
	return writeFileAtomic(SecretsPath(userMode), []byte(SecretsFileContent(deviceKey)), 0o600)
}

// ReadDeviceKey returns the stored device key, for the credential check.
func ReadDeviceKey(userMode bool) (string, error) {
	data, err := os.ReadFile(SecretsPath(userMode))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotEnrolled
		}
		return "", err
	}
	var secrets map[string]string
	if err := json.Unmarshal(data, &secrets); err != nil {
		return "", fmt.Errorf("secrets file %s is not valid JSON: %w", SecretsPath(userMode), err)
	}
	key := secrets[SecretsKey]
	if key == "" {
		return "", fmt.Errorf("secrets file %s has no %s", SecretsPath(userMode), SecretsKey)
	}
	return key, nil
}

// RemoveState deletes the managed-ingest directory. With keepCredentials the enrollment record
// and secrets file stay so a later connect can rotate in place without a new approval.
func RemoveState(userMode bool, keepCredentials bool) error {
	dir := Dir(userMode)
	if !keepCredentials {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	for _, path := range []string{VectorConfigPath(userMode), DataDir(userMode)} {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func ensureDir(userMode bool) error {
	dir := Dir(userMode)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing directory's mode alone; tighten it in case it was created
	// by an older tool or by hand.
	return os.Chmod(dir, 0o700)
}

// writeFileAtomic writes via a temp file in the same directory so a crash never leaves a
// half-written credential, and sets the mode before the rename so the content is never
// readable with wider permissions.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
