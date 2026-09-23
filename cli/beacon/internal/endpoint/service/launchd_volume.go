package service

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A user whose home directory lives on an external volume (`/Volumes/<disk>`, with NFSHomeDirectory
// pointing there) gets every Beacon LaunchAgent refused by launchd: `launchctl bootstrap gui/<uid>
// ~/Library/LaunchAgents/<label>.plist` fails with "Bootstrap failed: 5: Input/output error", the
// install rolls back, and the inventory job never loads (#639). The same plist bootstraps cleanly
// once it is copied to the startup volume, and the job then runs normally with its program and
// config still on the external volume -- so the refusal is about where the job definition is read
// from, not about what the job touches. An independent report against another launchd-managed
// tool shows the same failure and the same cure, with `diskutil enableOwnership` making no
// difference.
//
// Beacon therefore keeps writing the canonical plist to ~/Library/LaunchAgents, where an operator
// and launchd's own login scan expect it, and hands launchctl a byte-identical copy staged in a
// private directory on the startup volume whenever the LaunchAgents directory is on a different
// volume. A home on the startup volume is untouched: its plist is bootstrapped in place as before.

// Seams for tests: which volume a path is on, the path that stands for the startup volume, and
// where a staged copy may go. /Users is on the startup volume's Data half on every supported macOS
// (it is a firmlink into /System/Volumes/Data), which is where both TMPDIR and /private/tmp live;
// "/" itself is the sealed system volume and has a different device number, so it cannot be the
// reference.
var (
	launchdVolumeID      = volumeID
	launchdBootVolumeRef = "/Users"
	launchdStagingBases  = func() []string { return []string{os.TempDir(), "/private/tmp"} }
)

// LaunchAgentVolume describes where a user LaunchAgent's plist sits relative to the startup volume.
type LaunchAgentVolume struct {
	// PlistDir is the LaunchAgents directory that holds the canonical plist.
	PlistDir string `json:"plist_dir"`
	// External is true when PlistDir is on a volume other than the startup volume.
	External bool `json:"external"`
	// StagingDir is the private startup-volume directory the plist is bootstrapped from when
	// External is true. Empty when External is false, or when no usable location was found.
	StagingDir string `json:"staging_dir,omitempty"`
	// Reason says how External was decided, for diagnostics.
	Reason string `json:"reason,omitempty"`
}

// InspectLaunchAgentVolume reports whether plistPath is off the startup volume and, if so, where
// Beacon will stage the copy it bootstraps.
func InspectLaunchAgentVolume(plistPath string) LaunchAgentVolume {
	dir := filepath.Dir(plistPath)
	vol := LaunchAgentVolume{PlistDir: dir}
	bootID, bootErr := launchdVolumeID(launchdBootVolumeRef)
	dirID, dirErr := launchdVolumeID(dir)
	switch {
	case bootErr == nil && dirErr == nil:
		vol.External = dirID != bootID
		if vol.External {
			vol.Reason = fmt.Sprintf("%s is on a different volume than %s", dir, launchdBootVolumeRef)
		}
	case underVolumes(dir):
		// No device numbers to compare, so the mount point is the evidence: external and
		// secondary internal volumes are mounted under /Volumes.
		vol.External = true
		vol.Reason = dir + " is under /Volumes"
	}
	if !vol.External {
		return vol
	}
	for _, base := range launchdStagingBases() {
		if strings.TrimSpace(base) == "" {
			continue
		}
		if bootErr == nil {
			if id, err := launchdVolumeID(base); err != nil || id != bootID {
				continue
			}
		} else if underVolumes(base) {
			continue
		}
		vol.StagingDir = filepath.Join(base, launchdStagingDirName())
		break
	}
	return vol
}

// underVolumes reports whether p is under the macOS /Volumes mount point. launchd paths are POSIX
// paths, so the comparison is done with forward slashes whatever the host's separator is.
func underVolumes(p string) bool {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return strings.HasPrefix(path.Clean(filepath.ToSlash(p))+"/", "/Volumes/")
}

// launchdStagingDirName carries the uid so the /private/tmp fallback cannot collide between users.
func launchdStagingDirName() string {
	return fmt.Sprintf("beacon-launchd-%d", os.Getuid())
}

// launchdBootstrapPath returns the path launchctl should bootstrap for plistPath in domain, and a
// note to attach to a bootstrap failure. Only user domains are staged: LaunchDaemons live in
// /Library on the startup volume and are root's.
func launchdBootstrapPath(domain, plistPath string) (string, string) {
	if !strings.HasPrefix(domain, "gui/") {
		return plistPath, ""
	}
	vol := InspectLaunchAgentVolume(plistPath)
	if !vol.External {
		return plistPath, ""
	}
	if vol.StagingDir == "" {
		return plistPath, externalVolumeNote(domain, vol, "", errors.New("no private directory on the startup volume was found"))
	}
	staged, err := stageLaunchdPlist(plistPath, vol.StagingDir)
	if err != nil {
		return plistPath, externalVolumeNote(domain, vol, "", err)
	}
	return staged, externalVolumeNote(domain, vol, staged, nil)
}

// externalVolumeNote explains a bootstrap failure for a plist off the startup volume. It is only
// shown when launchctl fails, so it always ends with the manual route from #639.
func externalVolumeNote(domain string, vol LaunchAgentVolume, staged string, stageErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The LaunchAgents directory %s is not on the startup volume (%s); launchd refuses to bootstrap LaunchAgents from a home directory on an external volume.", vol.PlistDir, vol.Reason)
	if stageErr != nil {
		fmt.Fprintf(&b, " A copy could not be staged on the startup volume: %v.", stageErr)
	} else {
		fmt.Fprintf(&b, " Beacon bootstrapped a copy staged at %s, and launchd refused that too.", staged)
	}
	fmt.Fprintf(&b, " To load it by hand: install with --no-start, copy the plist from %s into a private directory on the startup volume (mkdir -p -m 700 \"$TMPDIR/beacon-launchd\"), then run `launchctl bootstrap %s <copied plist>`.", vol.PlistDir, domain)
	return b.String()
}

// stageLaunchdPlist copies plistPath into stagingDir under the same name and returns the copy's path.
//
// The directory is a fixed name in a temp directory, so it has to be one this user owns and nobody
// else can write, or another account could replace the job definition between the copy and the
// bootstrap and have launchd run its program as this user. The copy is written to a temporary name
// and renamed into place, which replaces rather than follows anything planted at the final name.
func stageLaunchdPlist(plistPath, stagingDir string) (string, error) {
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(stagingDir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := checkPrivateStagingDir(stagingDir); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(stagingDir, ".stage-*.plist")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	// launchd refuses a job definition that is group- or world-writable.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	dest := filepath.Join(stagingDir, filepath.Base(plistPath))
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// checkPrivateStagingDir refuses anything but a real directory owned by this user with no group or
// other access.
func checkPrivateStagingDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if uid, ok := fileOwner(info); ok && uid != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not this user", dir, uid)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is mode %o; it must not be accessible to other users", dir, perm)
	}
	return nil
}

// removeStagedLaunchdPlist deletes any staged copy of label's plist. launchd holds a job's
// definition in memory once it is bootstrapped, so the copy only matters for the next bootstrap,
// which restages it; removing it on bootout keeps an uninstall from leaving it behind.
func removeStagedLaunchdPlist(label string) {
	for _, base := range launchdStagingBases() {
		if strings.TrimSpace(base) == "" {
			continue
		}
		dir := filepath.Join(base, launchdStagingDirName())
		if checkPrivateStagingDir(dir) != nil {
			continue
		}
		_ = os.Remove(filepath.Join(dir, label+".plist"))
	}
}

// bootoutLaunchdJob stops and deregisters a job by label, tolerating one that is not loaded, and
// removes a user job's staged plist.
func bootoutLaunchdJob(domain, label string) error {
	err := runLaunchctlWithContext(domain, label, "", "bootout", domain+"/"+label)
	if strings.HasPrefix(domain, "gui/") {
		removeStagedLaunchdPlist(label)
	}
	return err
}
