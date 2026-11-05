package sentinel

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

// The managed forwarder: Vector running the Logs Ingestion config as a system service, set up by
// `beacon endpoint sentinel forwarder install --system`. Its state lives in one directory under the
// system base directory, apart from config.json, so a repair, which rebuilds config.json from its
// flags, never touches it. The directory is root-only (POSIX) or SYSTEM and Administrators only
// (Windows), because it holds the Entra credential.

const (
	// CredentialClientSecret and CredentialCertificate are the two app registration credentials.
	CredentialClientSecret = "client_secret"
	CredentialCertificate  = "client_certificate"

	settingsFileName    = "forwarder.json"
	secretsFileName     = "secrets.json"
	certificateFileName = "client.pfx"
	configFileName      = "vector.toml"
	dataDirName         = "data"
	logDirName          = "logs"

	// secretBackend is the Vector secret backend the config reads the credential through.
	secretBackend = "beacon_sentinel"

	settingsVersion = 1

	// vectorLogRetention is how long Vector's own daily warning logs are kept.
	vectorLogRetention = 7 * 24 * time.Hour
)

// ForwarderSpec is how the forwarder runs under each service manager. Vector drains for at most
// ten seconds on stop, and the Windows stop budget outlasts that, so a stop never gives up on a
// Vector that is still flushing. AZURE_AUTHORITY_HOST moves the Entra token authority, for a
// sovereign cloud.
func ForwarderSpec(authorityHost string) service.ForwarderSpec {
	spec := service.ForwarderSpec{
		Label:          "com.beacon.endpoint.sentinel-forwarder",
		SystemdUnit:    "beacon-sentinel-forwarder.service",
		WindowsService: "BeaconSentinelForwarder",
		Description:    "Beacon endpoint forwarder to Microsoft Sentinel",
		Documentation:  "https://docs.beacon.sh/log-forwarding/microsoft-sentinel",
		Args:           []string{"--graceful-shutdown-limit-secs", "10"},
		StopTimeout:    30 * time.Second,
	}
	if authorityHost != "" {
		spec.Env = []string{"AZURE_AUTHORITY_HOST=" + authorityHost}
	}
	return spec
}

// Settings is forwarder.json: everything needed to render the config again, and nothing secret.
type Settings struct {
	Version        int    `json:"version"`
	Endpoint       string `json:"endpoint"`
	DCRImmutableID string `json:"dcr_immutable_id"`
	TenantID       string `json:"tenant_id"`
	ClientID       string `json:"client_id"`
	Credential     string `json:"credential"`
	AuthorityHost  string `json:"authority_host,omitempty"`
	ReadFrom       string `json:"read_from"`
	LogPath        string `json:"log_path"`
	InstalledAt    string `json:"installed_at"`
}

// InstallOptions is what `forwarder install` was given.
type InstallOptions struct {
	Endpoint       string
	DCRImmutableID string
	TenantID       string
	ClientID       string
	// ClientSecret, or CertificatePath (a PFX file, copied into the forwarder's directory) with an
	// optional CertificatePassword. Exactly one of ClientSecret and CertificatePath.
	ClientSecret        string
	CertificatePath     string
	CertificatePassword string
	AuthorityHost       string
	// ReadFrom is where Vector starts in a log it has no checkpoint for: "beginning" (the default)
	// sends the current log once, "end" only what is written from now on.
	ReadFrom string
	LogPath  string
	// VectorBin names a Vector binary ahead of the usual search.
	VectorBin string
}

// InstallResult reports what install set up.
type InstallResult struct {
	Dir     string         `json:"dir"`
	Config  string         `json:"config"`
	Unit    string         `json:"unit"`
	Vector  string         `json:"vector"`
	Service service.Status `json:"service"`
}

// forwarderService is what the forwarder needs from service.ForwarderManager.
type forwarderService interface {
	Supported() bool
	UnsupportedReason() string
	WriteUnit(vectorBin, configPath string) (string, error)
	UnitCurrent(vectorBin, configPath string) (bool, error)
	Load() error
	Unload() error
	RemoveUnits()
	Status() service.Status
}

// Seams for tests: no service manager, no real Vector, no system directory.
var (
	newForwarderService = func(spec service.ForwarderSpec) forwarderService {
		return service.ForwarderManager{Spec: spec}
	}
	findVector     = asymptote.FindVector
	validateVector = asymptote.ValidateVectorConfig
	ForwarderDir   = func() string {
		return filepath.Join(endpointconfig.SystemBaseDir(), "forwarders", "sentinel")
	}
	now = time.Now
)

var (
	dcrIDPattern = regexp.MustCompile(`^dcr-[0-9a-f]{32}$`)
	guidPattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`)
)

// ErrNotInstalled is returned by Remove and Status when no forwarder was set up.
var ErrNotInstalled = errors.New("the Sentinel forwarder is not installed; set it up with `beacon endpoint sentinel forwarder install --system`")

func (o InstallOptions) settings() (Settings, error) {
	s := Settings{
		Version:        settingsVersion,
		Endpoint:       strings.TrimRight(strings.TrimSpace(o.Endpoint), "/"),
		DCRImmutableID: strings.TrimSpace(o.DCRImmutableID),
		TenantID:       strings.TrimSpace(o.TenantID),
		ClientID:       strings.TrimSpace(o.ClientID),
		AuthorityHost:  strings.TrimRight(strings.TrimSpace(o.AuthorityHost), "/"),
		ReadFrom:       strings.TrimSpace(o.ReadFrom),
		LogPath:        o.LogPath,
	}
	if err := httpsURL("--endpoint", s.Endpoint, true); err != nil {
		return s, err
	}
	if !dcrIDPattern.MatchString(s.DCRImmutableID) {
		return s, fmt.Errorf("--dcr-immutable-id %q is not a DCR immutable ID (dcr- followed by 32 hex digits)", s.DCRImmutableID)
	}
	if !guidPattern.MatchString(s.TenantID) {
		return s, fmt.Errorf("--tenant-id %q is not a GUID", s.TenantID)
	}
	if !guidPattern.MatchString(s.ClientID) {
		return s, fmt.Errorf("--client-id %q is not a GUID", s.ClientID)
	}
	if s.AuthorityHost != "" {
		if err := httpsURL("--authority-host", s.AuthorityHost, false); err != nil {
			return s, err
		}
	}
	switch s.ReadFrom {
	case "":
		s.ReadFrom = "beginning"
	case "beginning", "end":
	default:
		return s, fmt.Errorf("--read-from must be beginning or end, not %q", s.ReadFrom)
	}
	switch {
	case o.ClientSecret != "" && o.CertificatePath != "":
		return s, errors.New("give a client secret or a certificate, not both")
	case o.ClientSecret != "":
		s.Credential = CredentialClientSecret
	case o.CertificatePath != "":
		s.Credential = CredentialCertificate
	default:
		return s, errors.New("no credential: set AZURE_CLIENT_SECRET (or pass --client-secret-stdin), or pass --client-certificate with a PFX file")
	}
	if strings.TrimSpace(s.LogPath) == "" {
		return s, errors.New("no runtime log path")
	}
	return s, nil
}

// httpsURL accepts an https URL with a host, and with a path only where one makes sense.
func httpsURL(flag, raw string, bare bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s %q must be an https URL", flag, raw)
	}
	if bare && u.Path != "" {
		return fmt.Errorf("%s %q must be the DCR's logs ingestion URL alone, with no path", flag, raw)
	}
	return nil
}

type renderOptions struct {
	Settings               Settings
	DataDir                string
	SecretsPath            string
	CertificatePath        string
	LogDir                 string
	HasCertificatePassword bool
}

// renderForwarderConfig fills the pack's template with literal values, the credential read through
// Vector's file secret backend, and Vector's own warnings sent to a daily log.
func renderForwarderConfig(o renderOptions) string {
	s := o.Settings
	q := func(v string) string { return `"` + siempack.TOMLEscape(v) + `"` }
	var auth strings.Builder
	switch s.Credential {
	case CredentialCertificate:
		auth.WriteString("azure_credential_kind = \"client_certificate_credential\"\n")
		fmt.Fprintf(&auth, "azure_tenant_id = %s\nazure_client_id = %s\n", q(s.TenantID), q(s.ClientID))
		fmt.Fprintf(&auth, "certificate_path = %s\n", q(o.CertificatePath))
		if o.HasCertificatePassword {
			fmt.Fprintf(&auth, "certificate_password = \"SECRET[%s.certificate_password]\"\n", secretBackend)
		}
	default:
		auth.WriteString("azure_credential_kind = \"client_secret_credential\"\n")
		fmt.Fprintf(&auth, "azure_tenant_id = %s\nazure_client_id = %s\n", q(s.TenantID), q(s.ClientID))
		fmt.Fprintf(&auth, "azure_client_secret = \"SECRET[%s.client_secret]\"\n", secretBackend)
	}
	var secrets string
	if s.Credential == CredentialClientSecret || o.HasCertificatePassword {
		secrets = fmt.Sprintf("\n[secret.%s]\ntype = \"file\"\npath = %s\n", secretBackend, q(o.SecretsPath))
	}
	vectorLog := fmt.Sprintf(`
# Vector's own warnings and errors, one file a day, kept for a week. A Windows service has nowhere
# else to write them.
[sources.beacon_vector_logs]
type = "internal_logs"

[transforms.beacon_vector_problems]
type = "filter"
inputs = ["beacon_vector_logs"]
condition = '.metadata.level == "WARN" || .metadata.level == "ERROR"'

[sinks.beacon_vector_log]
type = "file"
inputs = ["beacon_vector_problems"]
path = %s

[sinks.beacon_vector_log.encoding]
codec = "json"
`, q(filepath.Join(o.LogDir, "vector-%Y-%m-%d.log")))
	note := "# Written by `beacon endpoint sentinel forwarder install`, which also runs it as a service.\n" +
		"# Run that again to change a setting: install, repair and upgrades replace edits made here."
	return fillTemplate(mustRead("pack/vector.toml.tmpl"), map[string]string{
		"{{SETTINGS_NOTE}}":    note,
		"{{DATA_DIR}}":         siempack.TOMLEscape(o.DataDir),
		"{{SECRET_BACKEND}}":   secrets,
		"{{READ_FROM}}":        s.ReadFrom,
		"{{ENDPOINT}}":         siempack.TOMLEscape(s.Endpoint),
		"{{DCR_IMMUTABLE_ID}}": siempack.TOMLEscape(s.DCRImmutableID),
		"{{AUTH}}":             auth.String(),
		"{{VECTOR_LOG}}":       vectorLog,
	}, s.LogPath)
}

// manualVectorConfig is the pack's vector.toml for running Vector by hand: values come from the
// Vector service environment, and {{LOG_PATH}} is left for the pack installer.
func manualVectorConfig() string {
	note := `# Set in the Vector service environment, not in Beacon config:
#   BEACON_SENTINEL_DCE_ENDPOINT      https://<dcr>.<region>-1.ingest.monitor.azure.com (the DCR's
#                                     logs ingestion URL, or a Data Collection Endpoint's)
#   BEACON_SENTINEL_DCR_IMMUTABLE_ID  dcr-... (the DCR's immutableId)
#   AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET
# Or let ` + "`beacon endpoint sentinel forwarder install --system`" + ` write this config with the values
# in it and run Vector as a service.`
	auth := `azure_credential_kind = "client_secret_credential"
azure_tenant_id = "${AZURE_TENANT_ID}"
azure_client_id = "${AZURE_CLIENT_ID}"
azure_client_secret = "${AZURE_CLIENT_SECRET}"
`
	return fillTemplate(mustRead("pack/vector.toml.tmpl"), map[string]string{
		"{{SETTINGS_NOTE}}":    note,
		"{{DATA_DIR}}":         "/var/lib/vector/beacon-sentinel",
		"{{SECRET_BACKEND}}":   "",
		"{{READ_FROM}}":        "end",
		"{{ENDPOINT}}":         "${BEACON_SENTINEL_DCE_ENDPOINT}",
		"{{DCR_IMMUTABLE_ID}}": "${BEACON_SENTINEL_DCR_IMMUTABLE_ID}",
		"{{AUTH}}":             auth,
		"{{VECTOR_LOG}}":       "",
	}, "")
}

func fillTemplate(tmpl string, values map[string]string, logPath string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, 2*len(keys))
	for _, key := range keys {
		pairs = append(pairs, key, values[key])
	}
	out := strings.NewReplacer(pairs...).Replace(tmpl)
	if logPath != "" {
		out = siempack.RenderLogPath(configFileName, out, logPath)
	}
	return out
}

func paths(dir string) (settings, secrets, certificate, config, data, logs string) {
	return filepath.Join(dir, settingsFileName), filepath.Join(dir, secretsFileName),
		filepath.Join(dir, certificateFileName), filepath.Join(dir, configFileName),
		filepath.Join(dir, dataDirName), filepath.Join(dir, logDirName)
}

// Install sets up the forwarder, or replaces one already set up. Nothing on disk or in the service
// manager changes until Vector has accepted the new config, so a rejected install leaves a working
// forwarder as it was. The caller has checked for system privileges.
func Install(o InstallOptions) (InstallResult, error) {
	settings, err := o.settings()
	if err != nil {
		return InstallResult{}, err
	}
	manager := newForwarderService(ForwarderSpec(settings.AuthorityHost))
	if !manager.Supported() {
		return InstallResult{}, errors.New(manager.UnsupportedReason())
	}
	vector, err := findVector(o.VectorBin)
	if err != nil {
		return InstallResult{}, err
	}
	var certificate []byte
	if settings.Credential == CredentialCertificate {
		if certificate, err = readCertificate(o.CertificatePath); err != nil {
			return InstallResult{}, err
		}
	}
	secrets := map[string]string{}
	switch {
	case settings.Credential == CredentialClientSecret:
		secrets["client_secret"] = o.ClientSecret
	case o.CertificatePassword != "":
		secrets["certificate_password"] = o.CertificatePassword
	}
	secretsJSON, err := json.Marshal(secrets)
	if err != nil {
		return InstallResult{}, err
	}

	dir := ForwarderDir()
	if err := ensurePrivateDir(dir); err != nil {
		return InstallResult{}, err
	}
	settingsPath, secretsPath, certificatePath, configPath, dataDir, logDir := paths(dir)

	// Proved in a scratch directory first, with the real credential so the secret backend is
	// exercised too, and nothing outside it touched.
	scratch, err := os.MkdirTemp(dir, ".preflight-*")
	if err != nil {
		return InstallResult{}, err
	}
	defer os.RemoveAll(scratch)
	preflight := renderOptions{
		Settings:               settings,
		DataDir:                filepath.Join(scratch, dataDirName),
		SecretsPath:            filepath.Join(scratch, secretsFileName),
		CertificatePath:        filepath.Join(scratch, certificateFileName),
		LogDir:                 filepath.Join(scratch, logDirName),
		HasCertificatePassword: o.CertificatePassword != "",
	}
	if err := writeFiles(preflight, secretsJSON, certificate); err != nil {
		return InstallResult{}, err
	}
	preflightConfig := filepath.Join(scratch, configFileName)
	if err := atomicfile.Write(preflightConfig, []byte(renderForwarderConfig(preflight)), 0o600); err != nil {
		return InstallResult{}, err
	}
	if err := validateVector(vector.Path, preflightConfig); err != nil {
		return InstallResult{}, err
	}

	final := renderOptions{
		Settings:               settings,
		DataDir:                dataDir,
		SecretsPath:            secretsPath,
		CertificatePath:        certificatePath,
		LogDir:                 logDir,
		HasCertificatePassword: o.CertificatePassword != "",
	}
	if err := writeFiles(final, secretsJSON, certificate); err != nil {
		return InstallResult{}, err
	}
	if settings.Credential != CredentialCertificate {
		_ = os.Remove(certificatePath)
	}
	if err := atomicfile.Write(configPath, []byte(renderForwarderConfig(final)), 0o600); err != nil {
		return InstallResult{}, err
	}
	settings.InstalledAt = now().UTC().Format(time.RFC3339)
	settingsJSON, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return InstallResult{}, err
	}
	if err := atomicfile.Write(settingsPath, append(settingsJSON, '\n'), 0o600); err != nil {
		return InstallResult{}, err
	}
	unit, err := manager.WriteUnit(vector.Path, configPath)
	if err != nil {
		return InstallResult{}, err
	}
	if err := manager.Load(); err != nil {
		return InstallResult{}, fmt.Errorf("%w; Vector's own log is in %s", err, logDir)
	}
	return InstallResult{Dir: dir, Config: configPath, Unit: unit, Vector: vector.Path, Service: manager.Status()}, nil
}

// writeFiles writes the credential files and the directories the config points at.
func writeFiles(o renderOptions, secretsJSON, certificate []byte) error {
	for _, dir := range []string{o.DataDir, o.LogDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if o.Settings.Credential == CredentialClientSecret || o.HasCertificatePassword {
		if err := sysacl.WriteSecretFile(o.SecretsPath, secretsJSON); err != nil {
			return fmt.Errorf("write %s: %w", o.SecretsPath, err)
		}
	} else {
		_ = os.Remove(o.SecretsPath)
	}
	if certificate != nil {
		if err := sysacl.WriteSecretFile(o.CertificatePath, certificate); err != nil {
			return fmt.Errorf("write %s: %w", o.CertificatePath, err)
		}
	}
	return nil
}

func readCertificate(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("--client-certificate: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("--client-certificate %s must be a PFX file of at most 1 MB", path)
	}
	return os.ReadFile(path)
}

// ensurePrivateDir creates dir readable only by root, or by SYSTEM and Administrators.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return sysacl.SecureSecretDir(dir)
}

// ReadSettings returns forwarder.json, or ErrNotInstalled.
func ReadSettings() (Settings, error) {
	settingsPath, _, _, _, _, _ := paths(ForwarderDir())
	data, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, ErrNotInstalled
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("%s is not valid JSON: %w", settingsPath, err)
	}
	return s, nil
}

// Remove stops and deletes the service. keepState leaves the directory, so a later install or
// upgrade brings the forwarder back with its credential and read positions: the MSI upgrade
// removes the old package this way before installing the new one.
func Remove(keepState bool) error {
	settings, err := ReadSettings()
	if err != nil && !errors.Is(err, ErrNotInstalled) {
		// Unreadable settings must not keep the service running; remove it with the default name.
		settings = Settings{}
	}
	manager := newForwarderService(ForwarderSpec(settings.AuthorityHost))
	unloadErr := manager.Unload()
	manager.RemoveUnits()
	if keepState {
		return unloadErr
	}
	if err := os.RemoveAll(ForwarderDir()); err != nil {
		return errors.Join(unloadErr, err)
	}
	return unloadErr
}

// ReconcileResult reports what Reconcile did.
type ReconcileResult struct {
	Skipped   string `json:"skipped,omitempty"`
	Unchanged bool   `json:"unchanged,omitempty"`
	Restarted bool   `json:"restarted,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

// Message is a one-line summary for install, repair and the reconcile command.
func (r ReconcileResult) Message() string {
	switch {
	case r.Skipped != "":
		return "Sentinel forwarder: " + r.Skipped
	case r.Warning != "":
		return "Sentinel forwarder: " + r.Warning
	case r.Unchanged:
		return "Sentinel forwarder: running, unchanged"
	default:
		return "Sentinel forwarder: restarted"
	}
}

// ReconcileOptions drives Reconcile.
type ReconcileOptions struct {
	// LogPath is the endpoint's runtime log; empty keeps the one recorded at install.
	LogPath string
	// Restart restarts a running forwarder even when nothing about it changed. Install and repair
	// set it, because they may have replaced the log with a copy under the same name (switching
	// log access does), and Vector, which recognizes a file by its first line, keeps reading the
	// replaced file until it restarts. After a restart it resumes in the copy where it left off.
	Restart bool
}

// Reconcile brings an installed forwarder up to date with this version: it re-renders the config
// from the stored settings and the pack this binary carries, has Vector validate it, rewrites the
// service definition if it changed, and makes sure the service is running. Install, repair and
// package upgrades run it, which is also how the MSI upgrade puts back the service its uninstall
// step removed. A config Vector rejects is a warning that keeps the old one.
func Reconcile(opts ReconcileOptions) (ReconcileResult, error) {
	logPath := opts.LogPath
	settings, err := ReadSettings()
	if errors.Is(err, ErrNotInstalled) {
		return ReconcileResult{Skipped: "not installed"}, nil
	}
	if err != nil {
		return ReconcileResult{Warning: err.Error()}, err
	}
	manager := newForwarderService(ForwarderSpec(settings.AuthorityHost))
	if !manager.Supported() {
		return ReconcileResult{Skipped: manager.UnsupportedReason()}, nil
	}
	vector, err := findVector("")
	if err != nil {
		return ReconcileResult{Warning: err.Error()}, err
	}
	dir := ForwarderDir()
	if err := ensurePrivateDir(dir); err != nil {
		return ReconcileResult{Warning: err.Error()}, err
	}
	pruneVectorLogs(filepath.Join(dir, logDirName))
	if logPath != "" {
		settings.LogPath = logPath
	}
	_, secretsPath, certificatePath, configPath, dataDir, logDir := paths(dir)
	hasPassword := false
	if settings.Credential == CredentialCertificate {
		if _, err := os.Stat(secretsPath); err == nil {
			hasPassword = true
		}
	}
	for _, d := range []string{dataDir, logDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return ReconcileResult{Warning: err.Error()}, err
		}
	}
	rendered := renderForwarderConfig(renderOptions{
		Settings:               settings,
		DataDir:                dataDir,
		SecretsPath:            secretsPath,
		CertificatePath:        certificatePath,
		LogDir:                 logDir,
		HasCertificatePassword: hasPassword,
	})
	current, _ := os.ReadFile(configPath)
	configChanged := string(current) != rendered
	if configChanged {
		candidate := configPath + ".new"
		if err := atomicfile.Write(candidate, []byte(rendered), 0o600); err != nil {
			return ReconcileResult{Warning: err.Error()}, err
		}
		if err := validateVector(vector.Path, candidate); err != nil {
			_ = os.Remove(candidate)
			return ReconcileResult{Warning: "kept the previous config: " + err.Error()}, nil
		}
		if err := os.Rename(candidate, configPath); err != nil {
			return ReconcileResult{Warning: err.Error()}, err
		}
	}
	unitCurrent, _ := manager.UnitCurrent(vector.Path, configPath)
	if !unitCurrent {
		if _, err := manager.WriteUnit(vector.Path, configPath); err != nil {
			return ReconcileResult{Warning: err.Error()}, err
		}
	}
	if !opts.Restart && !configChanged && unitCurrent && manager.Status().Running {
		return ReconcileResult{Unchanged: true}, nil
	}
	if err := manager.Load(); err != nil {
		return ReconcileResult{Warning: err.Error()}, err
	}
	return ReconcileResult{Restarted: true}, nil
}

// Status is what `forwarder status` and `endpoint status` show.
type Status struct {
	Installed bool           `json:"installed"`
	Settings  *Settings      `json:"settings,omitempty"`
	Service   service.Status `json:"service"`
	// LastProblem is the newest warning or error in Vector's own log, if any.
	LastProblem string `json:"last_problem,omitempty"`
}

// ReadStatus reports the forwarder's settings, service state and Vector's latest complaint.
func ReadStatus() Status {
	settings, err := ReadSettings()
	if err != nil {
		manager := newForwarderService(ForwarderSpec(""))
		status := Status{Service: manager.Status()}
		return status
	}
	manager := newForwarderService(ForwarderSpec(settings.AuthorityHost))
	return Status{
		Installed:   true,
		Settings:    &settings,
		Service:     manager.Status(),
		LastProblem: lastVectorProblem(filepath.Join(ForwarderDir(), logDirName)),
	}
}

// lastVectorProblem reads the last line of the newest daily log.
func lastVectorProblem(logDir string) string {
	matches, _ := filepath.Glob(filepath.Join(logDir, "vector-*.log"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	f, err := os.Open(matches[len(matches)-1])
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			last = line
		}
	}
	var entry struct {
		Timestamp string `json:"timestamp"`
		Message   string `json:"message"`
		Metadata  struct {
			Level string `json:"level"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(last), &entry) != nil || entry.Message == "" {
		return last
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s", entry.Timestamp, entry.Metadata.Level, entry.Message))
}

func pruneVectorLogs(logDir string) {
	matches, _ := filepath.Glob(filepath.Join(logDir, "vector-*.log"))
	cutoff := now().Add(-vectorLogRetention)
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}
