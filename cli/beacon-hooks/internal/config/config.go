package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Directories
var (
	BeaconDir   = getBeaconDir()
	ClaudeDir   = filepath.Join(BeaconDir, "claude")
	CopilotDir  = filepath.Join(BeaconDir, "copilot")
	CursorDir   = filepath.Join(BeaconDir, "cursor")
	DevinDir    = filepath.Join(BeaconDir, "devin")
	FactoryDir  = filepath.Join(BeaconDir, "factory")
	GrokDir     = filepath.Join(BeaconDir, "grok")
	OpenCodeDir = filepath.Join(BeaconDir, "opencode")
)

// Log rotation
const (
	LogMaxSizeBytes = 10 * 1024 * 1024 // 10 MB
)

type ContentRetention string

const (
	ContentRetentionMetadata ContentRetention = "metadata"
	ContentRetentionRedacted ContentRetention = "redacted"
	ContentRetentionFull     ContentRetention = "full"
)

var SystemEndpointConfigPath = "/Library/Application Support/Beacon/Endpoint/config.json"

// Scannable extensions
var scannableExtensions = map[string]bool{
	// JavaScript/TypeScript
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true,
	// Python
	".py": true, ".pyw": true,
	// Java/Kotlin
	".java": true, ".kt": true, ".kts": true,
	// Go
	".go": true,
	// Rust
	".rs": true,
	// C/C++
	".c": true, ".h": true, ".cpp": true, ".cc": true, ".cxx": true, ".hpp": true, ".hxx": true,
	// C#
	".cs": true,
	// Ruby
	".rb": true,
	// PHP
	".php": true,
	// Swift
	".swift": true,
	// Solidity
	".sol": true,
	// Shell
	".sh": true, ".bash": true, ".zsh": true,
	// SQL
	".sql": true,
	// YAML (for IaC)
	".yaml": true, ".yml": true,
}

func getBeaconDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".beacon"
	}
	return filepath.Join(homeDir, ".beacon")
}

// IsScannableFile checks if a file should be scanned based on its extension
func IsScannableFile(filePath string) bool {
	if filePath == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	return scannableExtensions[ext]
}

// GetStateDir returns the state directory for the given platform
func GetStateDir(platform string) string {
	switch platform {
	case "copilot":
		return CopilotDir
	case "cursor":
		return CursorDir
	case "devin":
		return DevinDir
	case "factory":
		return FactoryDir
	case "grok":
		return GrokDir
	case "opencode":
		return OpenCodeDir
	default:
		return ClaudeDir
	}
}

// GetLogFile returns the log file path for the given platform
func GetLogFile(platform string) string {
	return filepath.Join(GetStateDir(platform), "hooks.log")
}

// GetSessionLogDir returns the session logs directory for the given platform
func GetSessionLogDir(platform string) string {
	return filepath.Join(GetStateDir(platform), "logs")
}

// GetSessionLogFile returns the per-session log file path
func GetSessionLogFile(platform, sessionID string) string {
	return filepath.Join(GetSessionLogDir(platform), sessionID+".log")
}

// EnsureSessionLogDir creates the session logs directory if it doesn't exist
func EnsureSessionLogDir(platform string) error {
	return os.MkdirAll(GetSessionLogDir(platform), 0755)
}

// RotateLogIfNeededForPlatform clears the platform-specific log file if it exceeds LogMaxSizeBytes
func RotateLogIfNeededForPlatform(platform string) bool {
	logFile := GetLogFile(platform)
	info, err := os.Stat(logFile)
	if err != nil {
		return false
	}

	if info.Size() > LogMaxSizeBytes {
		os.WriteFile(logFile, []byte{}, 0644)
		return true
	}

	return false
}

// EnsureStateDir ensures the state directory for the given platform exists
func EnsureStateDir(platform string) error {
	return os.MkdirAll(GetStateDir(platform), 0755)
}

func ContentRetentionMode() ContentRetention {
	if mode, ok := parseContentRetention(os.Getenv("BEACON_CONTENT_RETENTION")); ok {
		return mode
	}
	for _, endpointPath := range endpointConfigPaths() {
		data, err := os.ReadFile(endpointPath)
		if err != nil {
			continue
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if mode, ok := parseContentRetention(cfg["content_retention"]); ok {
			return mode
		}
	}
	return ContentRetentionFull
}

func endpointConfigPaths() []string {
	if endpointPath := os.Getenv("BEACON_ENDPOINT_CONFIG"); endpointPath != "" {
		return []string{endpointPath}
	}
	userPath := filepath.Join(BeaconDir, "endpoint", "config.json")
	if usesSystemEndpointLog(os.Getenv("BEACON_ENDPOINT_LOG")) {
		return []string{SystemEndpointConfigPath, userPath}
	}
	return []string{userPath, SystemEndpointConfigPath}
}

func usesSystemEndpointLog(path string) bool {
	return strings.HasPrefix(path, "/var/log/") || strings.HasPrefix(path, "/Library/")
}

func parseContentRetention(value interface{}) (ContentRetention, bool) {
	mode, _ := value.(string)
	switch ContentRetention(mode) {
	case ContentRetentionMetadata:
		return ContentRetentionMetadata, true
	case ContentRetentionRedacted:
		return ContentRetentionRedacted, true
	case ContentRetentionFull:
		return ContentRetentionFull, true
	default:
		return ContentRetentionFull, false
	}
}
