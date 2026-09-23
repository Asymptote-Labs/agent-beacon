package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// The S3, GCS and Falcon forwarders are installed by the shell helpers the macOS package ships
// under /opt/beacon/jamf/claude/<name>/install-forwarder.sh, not by the CLI, so no manager here
// writes them. The CLI still has to stop and remove them: they are KeepAlive LaunchDaemons, and
// their env files hold the customer's AWS keys, GCS credential path or Falcon HEC token.
// Uninstall used to leave all of that behind, and only the Jamf full-cleanup script removed it.
var (
	ScriptForwarderLabels = []string{
		"com.beacon.endpoint.falcon-forwarder",
		"com.beacon.endpoint.s3-forwarder",
		"com.beacon.endpoint.gcs-forwarder",
	}
	// scriptForwarderDaemonsDir and scriptForwarderConfigDir are variables so tests can point
	// them at a temp dir; the helpers accept the same overrides (BEACON_LAUNCHDAEMONS_DIR,
	// BEACON_FORWARDER_BASE_DIR) for the same reason.
	scriptForwarderDaemonsDir = "/Library/LaunchDaemons"
	scriptForwarderConfigDir  = "/Library/Application Support/Beacon/Forwarders"
)

// ScriptForwarderConfigDir is where the package helpers keep each forwarder's env file, Vector
// config and buffered data.
func ScriptForwarderConfigDir() string {
	return scriptForwarderConfigDir
}

func scriptForwarderPlistPath(label string) string {
	return filepath.Join(scriptForwarderDaemonsDir, label+".plist")
}

// RemoveScriptForwarders stops every package-helper forwarder and deletes its LaunchDaemon.
// Unless keepConfig is set it also deletes their config directory, which holds each forwarder's
// env file, Vector config and buffered data. That mirrors how uninstall treats the managed-ingest
// forwarder: the service always goes, the credentials only when config is not being kept.
func RemoveScriptForwarders(keepConfig bool) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	var errs []error
	for _, label := range ScriptForwarderLabels {
		path := scriptForwarderPlistPath(label)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if err := bootoutLaunchdJob("system", label); err != nil {
			errs = append(errs, err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	if !keepConfig {
		if err := os.RemoveAll(scriptForwarderConfigDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RestoreScriptForwarders loads every package-helper forwarder whose LaunchDaemon is on disk.
//
// The package preinstall boots these out before an upgrade and the postinstall loads them again.
// A self-update that fails and rolls back never reaches that postinstall, so without this the
// forwarders stay stopped until the next reboot. A forwarder that is already loaded is reloaded,
// the same as a bootstrap the postinstall performs.
func RestoreScriptForwarders() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	var errs []error
	for _, label := range ScriptForwarderLabels {
		path := scriptForwarderPlistPath(label)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if err := bootstrapLaunchdJob("system", label, path); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", label, err))
		}
	}
	return errors.Join(errs...)
}
