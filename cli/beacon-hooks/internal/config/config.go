package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Directories
var (
	BeaconDir  = getBeaconDir()
	ClaudeDir  = filepath.Join(BeaconDir, "claude")
	CopilotDir = filepath.Join(BeaconDir, "copilot")
	CursorDir  = filepath.Join(BeaconDir, "cursor")
	FactoryDir = filepath.Join(BeaconDir, "factory")
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
	case "factory":
		return FactoryDir
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
	endpointPath := filepath.Join(BeaconDir, "endpoint", "config.json")
	data, err := os.ReadFile(endpointPath)
	if err != nil {
		return ContentRetentionMetadata
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ContentRetentionMetadata
	}
	mode, _ := cfg["content_retention"].(string)
	switch ContentRetention(mode) {
	case ContentRetentionRedacted:
		return ContentRetentionRedacted
	case ContentRetentionFull:
		return ContentRetentionFull
	default:
		return ContentRetentionMetadata
	}
}
