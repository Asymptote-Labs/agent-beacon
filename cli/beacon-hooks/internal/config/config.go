package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Directories
var (
	BeaconDir      = getBeaconDir()
	ClaudeDir      = filepath.Join(BeaconDir, "claude")
	AntigravityDir = filepath.Join(BeaconDir, "antigravity")
	CopilotDir     = filepath.Join(BeaconDir, "copilot")
	CursorDir      = filepath.Join(BeaconDir, "cursor")
	VSCodeDir      = filepath.Join(BeaconDir, "vscode")
	DevinDir       = filepath.Join(BeaconDir, "devin")
	FactoryDir     = filepath.Join(BeaconDir, "factory")
	GrokDir        = filepath.Join(BeaconDir, "grok")
	HermesDir      = filepath.Join(BeaconDir, "hermes")
	OpenCodeDir    = filepath.Join(BeaconDir, "opencode")
	QwenDir        = filepath.Join(BeaconDir, "qwen")
	ClineDir       = filepath.Join(BeaconDir, "cline")
)

// Log rotation
const (
	LogMaxSizeBytes   = 10 * 1024 * 1024 // 10 MB
	LogRotateArchives = 5
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
	case "antigravity":
		return AntigravityDir
	case "copilot":
		return CopilotDir
	case "cursor":
		return CursorDir
	case "vscode":
		return VSCodeDir
	case "devin", "devin-cli", "devin-desktop":
		return DevinDir
	case "factory":
		return FactoryDir
	case "grok":
		return GrokDir
	case "hermes":
		return HermesDir
	case "opencode":
		return OpenCodeDir
	case "qwen":
		return QwenDir
	case "cline":
		return ClineDir
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

// RotateLogIfNeededForPlatform rotates the platform-specific log file if it exceeds LogMaxSizeBytes.
func RotateLogIfNeededForPlatform(platform string) bool {
	logFile := GetLogFile(platform)
	info, err := os.Stat(logFile)
	if err != nil {
		return false
	}

	if info.Size() > LogMaxSizeBytes {
		return rotateLog(logFile, LogRotateArchives) == nil
	}

	return false
}

func rotateLog(path string, archives int) error {
	if archives < 1 {
		archives = LogRotateArchives
	}
	if err := removeOverflowArchives(path, archives); err != nil {
		return err
	}
	for i := archives - 1; i >= 1; i-- {
		from := path + fmt.Sprintf(".%d", i)
		to := path + fmt.Sprintf(".%d", i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}

func removeOverflowArchives(path string, archives int) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := base + "."
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index < archives {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// EnsureStateDir ensures the state directory for the given platform exists
func EnsureStateDir(platform string) error {
	return os.MkdirAll(GetStateDir(platform), 0755)
}
