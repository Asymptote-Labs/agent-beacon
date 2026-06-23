package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Mode controls self-updater behavior.
type Mode string

const (
	// ModeAuto checks for and applies updates seamlessly. This is the default.
	ModeAuto Mode = "auto"
	// ModeCheckOnly surfaces an available update but never applies it.
	ModeCheckOnly Mode = "check-only"
	// ModeOff disables the background updater entirely.
	ModeOff Mode = "off"
)

// DefaultMode is the built-in default when nothing overrides it.
const DefaultMode = ModeAuto

// AutoUpdateEnv overrides the resolved mode for a single invocation.
const AutoUpdateEnv = "BEACON_AUTO_UPDATE"

// ManagedConfigPath is where an MDM/enterprise profile can drop settings that
// override the local config (but not an explicit env override).
var ManagedConfigPath = filepath.Join(SystemSupportDir, "managed.json")

// ParseMode normalizes a mode string. It accepts the canonical values plus the
// legacy boolean forms (1/true/on => auto, 0/false/off/no => off).
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "1", "true", "on", "enable", "enabled":
		return ModeAuto, true
	case "check-only", "check", "checkonly":
		return ModeCheckOnly, true
	case "off", "0", "false", "no", "disable", "disabled":
		return ModeOff, true
	default:
		return "", false
	}
}

// managedConfig is the subset of the managed profile the updater reads.
type managedConfig struct {
	AutoUpdate *struct {
		Mode string `json:"mode"`
	} `json:"auto_update"`
	ManifestURL string `json:"manifest_url"`
}

func loadManagedConfig() (managedConfig, bool) {
	data, err := os.ReadFile(ManagedConfigPath)
	if err != nil {
		return managedConfig{}, false
	}
	var mc managedConfig
	if err := json.Unmarshal(data, &mc); err != nil {
		return managedConfig{}, false
	}
	return mc, true
}

// ResolveMode determines the effective mode using the precedence:
//  1. BEACON_AUTO_UPDATE env
//  2. managed config dropped by MDM/enterprise
//  3. the local config value (localMode)
//  4. DefaultMode
//
// A managed layer can therefore always override the local default without a
// code change, which keeps the door open for an enterprise update path.
func ResolveMode(localMode string) Mode {
	if env := os.Getenv(AutoUpdateEnv); strings.TrimSpace(env) != "" {
		if m, ok := ParseMode(env); ok {
			return m
		}
	}
	if mc, ok := loadManagedConfig(); ok && mc.AutoUpdate != nil {
		if m, ok := ParseMode(mc.AutoUpdate.Mode); ok {
			return m
		}
	}
	if m, ok := ParseMode(localMode); ok {
		return m
	}
	return DefaultMode
}

// ManagedManifestURL returns a manifest URL forced by the managed profile, if
// any. Empty means "use the built-in/env default".
func ManagedManifestURL() string {
	if mc, ok := loadManagedConfig(); ok {
		return strings.TrimSpace(mc.ManifestURL)
	}
	return ""
}
