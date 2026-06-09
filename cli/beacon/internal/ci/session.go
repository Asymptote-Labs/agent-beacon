package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	endpointcollector "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

const (
	DefaultValidationMin = 1
)

var (
	resolveCollectorBinary = endpointcollector.ResolveBinary
	writeCollectorConfig   = endpointcollector.WriteConfig
	waitCollectorReady     = endpointcollector.WaitUntilReady
	collectorCommand       = exec.Command
	childCommandContext    = exec.CommandContext
)

type Options struct {
	BaseDir           string
	LogPath           string
	WorkDir           string
	CollectorPath     string
	GRPCPort          int
	HTTPPort          int
	Harness           string
	KeepArtifacts     bool
	IncludeCodexSpans bool
	// Forward selects an optional SIEM forwarder ("splunk" or "falcon") for the
	// ephemeral collector. ForwardEndpoint supplies the destination URL; the
	// token is read from the environment only.
	Forward         string
	ForwardEndpoint string
	Uploads         []string
}

type Session struct {
	BaseDir         string          `json:"base_dir"`
	LogPath         string          `json:"log_path"`
	ConfigPath      string          `json:"config_path"`
	CollectorBinary string          `json:"collector_binary,omitempty"`
	GRPCEndpoint    string          `json:"grpc_endpoint"`
	HTTPEndpoint    string          `json:"http_endpoint"`
	Harness         string          `json:"harness"`
	Harnesses       []string        `json:"harnesses,omitempty"`
	WorkDir         string          `json:"work_dir,omitempty"`
	StartedAt       string          `json:"started_at"`
	StatePath       string          `json:"state_path,omitempty"`
	CollectorPID    int             `json:"collector_pid,omitempty"`
	Run             *schema.RunInfo `json:"run,omitempty"`
	// Forward and ForwardEndpoint describe the optional SIEM egress. The token
	// is never recorded here so it cannot leak into --json output.
	Forward         string              `json:"forward,omitempty"`
	ForwardEndpoint string              `json:"forward_endpoint,omitempty"`
	Uploads         []UploadDestination `json:"uploads,omitempty"`
	Env             map[string]string   `json:"env,omitempty"`
	Paths           map[string]string   `json:"paths,omitempty"`

	cfg       endpointconfig.Config
	startedAt time.Time
	cmd       *exec.Cmd
	done      chan error
}

type StartResult struct {
	Session         Session           `json:"session"`
	Exports         map[string]string `json:"exports,omitempty"`
	StatePath       string            `json:"state_path,omitempty"`
	ArtifactMessage string            `json:"artifact_message,omitempty"`
}

type FinishResult struct {
	Session         Session          `json:"session"`
	Validation      ValidationResult `json:"validation"`
	Uploads         []UploadResult   `json:"uploads,omitempty"`
	ArtifactMessage string           `json:"artifact_message,omitempty"`
}

type ExecResult struct {
	Session         Session          `json:"session"`
	ChildExitCode   int              `json:"child_exit_code"`
	Validation      ValidationResult `json:"validation"`
	Uploads         []UploadResult   `json:"uploads,omitempty"`
	ArtifactMessage string           `json:"artifact_message,omitempty"`
}

func Provision(opts Options) (*Session, error) {
	harnesses, err := NormalizeHarnesses(opts.Harness, DefaultHarness)
	if err != nil {
		return nil, err
	}
	baseDir, err := resolveBaseDir(opts.BaseDir, opts.LogPath)
	if err != nil {
		return nil, err
	}
	logPath := opts.LogPath
	if logPath == "" {
		logPath = filepath.Join(baseDir, "runtime.jsonl")
	} else if !filepath.IsAbs(logPath) {
		logPath, err = filepath.Abs(logPath)
		if err != nil {
			return nil, err
		}
	}
	grpcPort := opts.GRPCPort
	if grpcPort == 0 {
		grpcPort = endpointconfig.DefaultGRPCPort
	}
	httpPort := opts.HTTPPort
	if httpPort == 0 {
		httpPort = endpointconfig.DefaultHTTPPort
	}
	cfg := endpointconfig.Default(true, logPath)
	cfg.Harnesses = harnesses
	cfg.Collector.BinaryPath = opts.CollectorPath
	cfg.Collector.ConfigPath = filepath.Join(baseDir, "otelcol.yaml")
	cfg.Collector.SpoolPath = filepath.Join(baseDir, "spool", "otlp.jsonl")
	cfg.Collector.GRPCPort = grpcPort
	cfg.Collector.HTTPPort = httpPort
	cfg.Collector.IncludeCodexSpans = opts.IncludeCodexSpans
	forward, err := normalizeForward(opts.Forward)
	if err != nil {
		return nil, err
	}
	var forwardEndpoint string
	if forward != "" {
		destinations, err := resolveForwardDestinations(forward, opts.ForwardEndpoint)
		if err != nil {
			return nil, err
		}
		cfg.Destinations = destinations
		forwardEndpoint = forwardEndpointOf(destinations)
		// Replace raw tokens with OTel Collector env-var references so the
		// secret is never written to disk. The collector inherits the parent
		// environment and can resolve them; the child has these vars stripped.
		replaceTokensWithEnvRefs(&cfg)
	}
	startedAt := time.Now().UTC()
	run := detectRunInfo()
	uploads, err := resolveUploadDestinations(opts.Uploads, run, logPath, startedAt)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := writeCollectorConfig(cfg); err != nil {
		return nil, err
	}
	binary, err := resolveCollectorBinary(opts.CollectorPath)
	if err != nil {
		return nil, err
	}
	harnessCfg, err := BuildHarnessConfig(os.Environ(), HarnessesString(harnesses), fmt.Sprintf("http://127.0.0.1:%d", grpcPort), baseDir, run)
	if err != nil {
		return nil, err
	}
	return &Session{
		BaseDir:         baseDir,
		LogPath:         logPath,
		ConfigPath:      cfg.Collector.ConfigPath,
		CollectorBinary: binary,
		GRPCEndpoint:    fmt.Sprintf("http://127.0.0.1:%d", grpcPort),
		HTTPEndpoint:    fmt.Sprintf("http://127.0.0.1:%d", httpPort),
		Harness:         HarnessesString(harnesses),
		Harnesses:       harnesses,
		WorkDir:         opts.WorkDir,
		StartedAt:       startedAt.Format(time.RFC3339),
		Run:             run,
		Forward:         forward,
		ForwardEndpoint: forwardEndpoint,
		Uploads:         uploads,
		Env:             harnessCfg.Env,
		Paths:           harnessCfg.Paths,
		cfg:             cfg,
		startedAt:       startedAt,
	}, nil
}

func (s *Session) Start(ctx context.Context, stdout, stderr io.Writer) error {
	if s == nil {
		return errors.New("ci session is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	cmd := collectorCommand(s.CollectorBinary, "--config", s.ConfigPath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if cmd.Process != nil {
		s.CollectorPID = cmd.Process.Pid
	}
	s.cmd = cmd
	s.done = make(chan error, 1)
	go func() {
		s.done <- cmd.Wait()
	}()
	readyCh := make(chan error, 1)
	go func() {
		readyCh <- waitCollectorReady(s.cfg, 10*time.Second)
	}()
	var readyErr error
	select {
	case readyErr = <-readyCh:
	case <-ctx.Done():
		readyErr = ctx.Err()
	}
	if readyErr != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		stopErr := s.Stop(cleanupCtx)
		cleanupCancel()
		if stopErr != nil {
			return fmt.Errorf("%w; collector stop also failed: %v", readyErr, stopErr)
		}
		return readyErr
	}
	return nil
}

func (s *Session) StartDetached(ctx context.Context, statePath string, stdout, stderr io.Writer) (StartResult, error) {
	if err := s.Start(ctx, stdout, stderr); err != nil {
		return StartResult{}, err
	}
	if statePath == "" {
		statePath = DefaultStatePath()
	}
	s.StatePath = statePath
	if err := s.SaveState(statePath); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Stop(cleanupCtx)
		cancel()
		return StartResult{}, err
	}
	return StartResult{
		Session:         *s,
		Exports:         s.Exports(),
		StatePath:       statePath,
		ArtifactMessage: fmt.Sprintf("Beacon CI session started: log=%s config=%s state=%s", s.LogPath, s.ConfigPath, statePath),
	}, nil
}

func (s *Session) Stop(ctx context.Context) error {
	if s == nil || s.cmd == nil {
		return nil
	}
	if s.cmd.Process != nil {
		_ = terminateProcess(s.cmd.Process)
	}
	select {
	case err := <-s.done:
		if err != nil && !isExpectedStopError(err) {
			return err
		}
		return nil
	case <-ctx.Done():
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		return ctx.Err()
	}
}

func (s *Session) StopDetached(ctx context.Context) error {
	if s == nil || s.CollectorPID == 0 {
		return nil
	}
	process, err := os.FindProcess(s.CollectorPID)
	if err != nil {
		return err
	}
	if err := terminateProcess(process); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !processAlive(process) {
			return nil
		}
		select {
		case <-ctx.Done():
			_ = process.Kill()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Session) RunChild(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("child command is required after --")
	}
	cmd := childCommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = s.WorkDir
	childEnv := envMap(os.Environ())
	for key, value := range s.Exports() {
		if key == "OTEL_RESOURCE_ATTRIBUTES" && strings.TrimSpace(childEnv[key]) != "" && strings.TrimSpace(value) != "" {
			childEnv[key] = childEnv[key] + "," + value
			continue
		}
		childEnv[key] = value
	}
	env := flattenEnv(childEnv)
	// The agent never needs destination credentials; keep them out of the child process.
	stripKeys := append([]string{}, forwardTokenEnv...)
	if len(s.Uploads) > 0 {
		stripKeys = append(stripKeys, uploadCredentialEnv...)
	}
	cmd.Env = stripEnv(env, stripKeys...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

func (s *Session) Exports() map[string]string {
	out := map[string]string{}
	if s != nil {
		if len(s.Env) == 0 {
			harness := s.Harness
			if strings.TrimSpace(harness) == "" {
				harness = DefaultHarness
			}
			cfg, err := BuildHarnessConfig(nil, harness, s.GRPCEndpoint, s.BaseDir, s.Run)
			if err == nil {
				s.Env = cfg.Env
				s.Paths = cfg.Paths
			}
		}
		for key, value := range s.Env {
			out[key] = value
		}
		out["BEACON_CI_BASE_DIR"] = s.BaseDir
		out["BEACON_CI_CONFIG_PATH"] = s.ConfigPath
		out["BEACON_CI_LOG_PATH"] = s.LogPath
		if s.StatePath != "" {
			out["BEACON_CI_STATE_PATH"] = s.StatePath
		} else {
			out["BEACON_CI_STATE_PATH"] = DefaultStatePath()
		}
		if codexHome := s.Paths["codex_home"]; codexHome != "" {
			out["CODEX_HOME"] = codexHome
		}
	}
	return out
}

func (s *Session) SaveState(path string) error {
	if s == nil {
		return errors.New("ci session is nil")
	}
	if path == "" {
		path = DefaultStatePath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func LoadState(path string) (*Session, error) {
	if path == "" {
		path = DefaultStatePath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	session.startedAt = session.StartedAtTime()
	return &session, nil
}

func (s *Session) Config() endpointconfig.Config {
	if s == nil {
		return endpointconfig.Config{}
	}
	return s.cfg
}

func (s *Session) StartedAtTime() time.Time {
	if s == nil {
		return time.Time{}
	}
	if !s.startedAt.IsZero() {
		return s.startedAt
	}
	startedAt, _ := time.Parse(time.RFC3339, s.StartedAt)
	return startedAt
}

func resolveBaseDir(configured, logPath string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		if err := os.MkdirAll(configured, 0755); err != nil {
			return "", err
		}
		return filepath.Abs(configured)
	}
	if strings.TrimSpace(logPath) != "" {
		base := filepath.Dir(logPath)
		if err := os.MkdirAll(base, 0755); err != nil {
			return "", err
		}
		return filepath.Abs(base)
	}
	if runnerTemp := strings.TrimSpace(os.Getenv("RUNNER_TEMP")); runnerTemp != "" {
		base := filepath.Join(runnerTemp, "beacon")
		if err := os.MkdirAll(base, 0755); err != nil {
			return "", err
		}
		return filepath.Abs(base)
	}
	base := filepath.Join(os.TempDir(), "beacon")
	if err := os.MkdirAll(base, 0755); err != nil {
		return "", err
	}
	return filepath.Abs(base)
}

func DefaultStatePath() string {
	if runnerTemp := strings.TrimSpace(os.Getenv("RUNNER_TEMP")); runnerTemp != "" {
		return filepath.Join(runnerTemp, "beacon", "session.json")
	}
	return filepath.Join(os.TempDir(), "beacon", "session.json")
}

func DefaultLogPath() string {
	if runnerTemp := strings.TrimSpace(os.Getenv("RUNNER_TEMP")); runnerTemp != "" {
		return filepath.Join(runnerTemp, "beacon", "runtime.jsonl")
	}
	return filepath.Join(os.TempDir(), "beacon", "runtime.jsonl")
}

func detectRunInfo() *schema.RunInfo {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		eventName := os.Getenv("GITHUB_EVENT_NAME")
		info := &schema.RunInfo{
			Provider:   "github_actions",
			RunID:      os.Getenv("GITHUB_RUN_ID"),
			RunAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"),
			Workflow:   os.Getenv("GITHUB_WORKFLOW"),
			Job:        os.Getenv("GITHUB_JOB"),
			EventName:  eventName,
			Commit:     os.Getenv("GITHUB_SHA"),
			Repository: os.Getenv("GITHUB_REPOSITORY"),
			Actor:      os.Getenv("GITHUB_ACTOR"),
			Ephemeral:  true,
		}
		// Prefer the pull-request head ref as the human-readable branch; fall
		// back to the pushed ref's short name on non-PR events.
		if head := strings.TrimSpace(os.Getenv("GITHUB_HEAD_REF")); head != "" {
			info.Branch = head
		} else {
			info.Branch = os.Getenv("GITHUB_REF_NAME")
		}
		// Only populate PR fields when the ref is actually a pull-request ref.
		// pull_request_target sets GITHUB_REF to the base branch, not the
		// merge ref, so we guard on the ref shape rather than just the event.
		if isPullRequestEvent(eventName) {
			ref := os.Getenv("GITHUB_REF")
			if strings.HasPrefix(ref, "refs/pull/") {
				info.PR = ref
				info.PRNumber = parsePRNumber(ref)
			}
		}
		return info
	}
	if os.Getenv("CI") != "" {
		return &schema.RunInfo{Provider: "ci", Ephemeral: true}
	}
	return nil
}

func isPullRequestEvent(eventName string) bool {
	return eventName == "pull_request" || eventName == "pull_request_target"
}

// parsePRNumber extracts the pull-request number from a ref of the form
// refs/pull/<number>/merge (or /head). It returns an empty string when the ref
// is not a pull-request ref or the number is not purely numeric.
func parsePRNumber(ref string) string {
	const prefix = "refs/pull/"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	num, _, ok := strings.Cut(strings.TrimPrefix(ref, prefix), "/")
	if !ok || num == "" {
		return ""
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return num
}

func terminateProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		return process.Kill()
	}
	return process.Signal(syscall.SIGTERM)
}

func processAlive(process *os.Process) bool {
	if process == nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	err := process.Signal(syscall.Signal(0))
	return err == nil
}

func isExpectedStopError(err error) bool {
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	return strings.Contains(err.Error(), "signal: terminated") || strings.Contains(err.Error(), "killed")
}

func NewSessionEvent(action, message string, run *schema.RunInfo) schema.Event {
	return schema.NewEvent(schema.NewEventOptions{
		Action:       action,
		Category:     "ci",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: DefaultHarness},
		Message:      message,
		Origin:       schema.OriginCI,
		Run:          run,
	})
}
