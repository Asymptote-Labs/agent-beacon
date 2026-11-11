//go:build windows

package launchpkg

import (
	"path/filepath"
	"testing"
)

// A snapshot is scanned with no environment, yet the launcher still reads the user's config under
// AppData, so Beacon derives those locations from the home; an environment that names them wins.
func TestLocalRegistriesReadsAppDataWithoutTheEnvironment(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, "AppData", "Roaming", "pip", "pip.ini"), "[global]\nindex-url = https://pypi.acme.internal/simple\n")
	writeConfig(t, filepath.Join(home, "AppData", "Local", "pnpm", "config", "rc"), "registry=https://npm.acme.internal/\n")
	if reg := LocalRegistries(RegistryEnv{Home: home}); !reg.PyPI || !reg.NPM {
		t.Errorf("AppData config with no environment: %+v, want npm and PyPI private", reg)
	}
	elsewhere := t.TempDir()
	env := RegistryEnv{Home: home, Environ: []string{"APPDATA=" + elsewhere, "LOCALAPPDATA=" + elsewhere}}
	if reg := LocalRegistries(env); reg.PyPI || reg.NPM {
		t.Errorf("AppData named elsewhere by the environment: %+v, want the home's AppData unread", reg)
	}
}
