package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
}

func Run(cfg endpointconfig.Config) []Check {
	checks := []Check{
		checkFile("config", endpointconfig.ConfigPath(cfg.UserMode), true),
		checkFile("collector_config", cfg.Collector.ConfigPath, true),
		checkFile("runtime_log", cfg.LogPath, false),
		checkLogPermissions(cfg.LogPath),
	}
	if runtime.GOOS == "darwin" {
		checks = append(checks, checkFile("launchd_plist", launchPlistPath(cfg.UserMode), true))
	}
	return checks
}

func HasFailures(checks []Check) bool {
	for _, check := range checks {
		if check.Status != "ok" {
			return true
		}
	}
	return false
}

func checkFile(name, path string, required bool) Check {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return Check{Name: name, Status: "warn", Severity: "low", Message: fmt.Sprintf("%s does not exist yet", path)}
		}
		return Check{Name: name, Status: "fail", Severity: "medium", Message: err.Error()}
	}
	if info.IsDir() {
		return Check{Name: name, Status: "fail", Severity: "medium", Message: path + " is a directory"}
	}
	return Check{Name: name, Status: "ok", Severity: "info", Message: path}
}

func checkLogPermissions(path string) Check {
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: "runtime_log_permissions", Status: "warn", Severity: "low", Message: "runtime log not created yet"}
	}
	mode := info.Mode().Perm()
	if mode&0022 != 0 {
		return Check{Name: "runtime_log_permissions", Status: "fail", Severity: "high", Message: fmt.Sprintf("runtime log is group/world writable: %o", mode)}
	}
	if mode&0044 == 0 {
		return Check{Name: "runtime_log_permissions", Status: "warn", Severity: "low", Message: fmt.Sprintf("runtime log may not be readable by Wazuh: %o", mode)}
	}
	return Check{Name: "runtime_log_permissions", Status: "ok", Severity: "info", Message: fmt.Sprintf("mode %o", mode)}
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
