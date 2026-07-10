package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

type Check struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Action   string `json:"action,omitempty"`
}

const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"

	SeverityInfo   = "info"
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

func Run(cfg endpointconfig.Config) []Check {
	checks := []Check{
		checkFile("config", endpointconfig.ConfigPath(cfg.UserMode), true),
		checkFile("collector_config", cfg.Collector.ConfigPath, true),
		checkFile("runtime_log", cfg.LogPath, false),
		checkLogPermissions(cfg.LogPath),
		checkCollectorHealth(cfg),
	}
	if runtime.GOOS == "darwin" {
		checks = append(checks, checkFile("launchd_plist", launchPlistPath(cfg.UserMode), true))
	}
	return checks
}

func checkCollectorHealth(cfg endpointconfig.Config) Check {
	status := collector.CheckStatus(cfg)
	if status.HealthReady {
		return Check{Name: "collector_health", Target: fmt.Sprintf("127.0.0.1:%d", collector.HealthCheckPort), Status: StatusOK, Severity: SeverityInfo, Message: "collector health check is ready", Evidence: "health_check_ready"}
	}
	message := status.Message
	if message == "" {
		message = "collector health check is not ready"
	}
	return Check{Name: "collector_health", Target: fmt.Sprintf("127.0.0.1:%d", collector.HealthCheckPort), Status: StatusWarn, Severity: SeverityMedium, Message: message, Evidence: "health_check_unavailable"}
}

func HasFailures(checks []Check) bool {
	for _, check := range checks {
		if check.Status == StatusFail {
			return true
		}
	}
	return false
}

func checkFile(name, path string, required bool) Check {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return Check{Name: name, Target: path, Status: StatusWarn, Severity: SeverityLow, Message: fmt.Sprintf("%s does not exist yet", path), Evidence: "missing_optional_file"}
		}
		return Check{Name: name, Target: path, Status: StatusFail, Severity: SeverityMedium, Message: err.Error(), Evidence: "stat_failed"}
	}
	if info.IsDir() {
		return Check{Name: name, Target: path, Status: StatusFail, Severity: SeverityMedium, Message: path + " is a directory", Evidence: "target_is_directory"}
	}
	return Check{Name: name, Target: path, Status: StatusOK, Severity: SeverityInfo, Message: path, Evidence: "file_exists"}
}

func checkLogPermissions(path string) Check {
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusWarn, Severity: SeverityLow, Message: "runtime log not created yet", Evidence: "runtime_log_missing"}
	}
	mode := info.Mode().Perm()
	if mode&0222 == 0 {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusFail, Severity: SeverityHigh, Message: fmt.Sprintf("runtime log is not writable: %o", mode), Evidence: "not_writable"}
	}
	if mode&0044 == 0 {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusWarn, Severity: SeverityLow, Message: fmt.Sprintf("runtime log may not be readable by Wazuh: %o", mode), Evidence: "not_group_or_world_readable"}
	}
	return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK, Severity: SeverityInfo, Message: fmt.Sprintf("mode %o", mode), Evidence: fmt.Sprintf("mode_%o", mode)}
}

func launchPlistPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("Library", "LaunchAgents", service.UserLabel+".plist")
		}
		return filepath.Join(home, "Library", "LaunchAgents", service.UserLabel+".plist")
	}
	return filepath.Join("/Library/LaunchDaemons", service.SystemLabel+".plist")
}
