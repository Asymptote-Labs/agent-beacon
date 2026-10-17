package selfupdate

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pinnedReleaseKeys holds the OpenPGP fingerprints, one per line, of the keys allowed to sign a
// release's checksums.txt; two only while a key is rotated. It is empty until Beacon has a Linux
// release key, and while it is, a Linux update is verified against the manifest's SHA-256 only.
// Once set, a .deb or .rpm is installed only if a checksums.txt signed by one of these keys lists
// it. The release copy of packaging/linux/install.sh is stamped from the same file, so pinning a
// key is a change to release-key.fingerprint alone.
//
//go:embed release-key.fingerprint
var pinnedReleaseKeys string

var releaseKeyFingerprints = strings.Fields(pinnedReleaseKeys)

// releaseKeyAsset is the release's public key, exported from the signing key by release.yml. It
// is fetched from the release like checksums.txt, so it is trusted only for carrying the pinned
// fingerprint.
const releaseKeyAsset = "beacon-release-key.gpg"

// verifyReleaseSignature checks that checksums.txt beside the package is signed by a pinned key
// and lists the package's file name with the downloaded bytes' SHA-256.
func (a *Applier) verifyReleaseSignature(ctx context.Context, pkgURL, pkgPath string) error {
	i := strings.LastIndex(pkgURL, "/")
	if i < 0 {
		return fmt.Errorf("package URL %q has no file name", pkgURL)
	}
	base, name := pkgURL[:i], pkgURL[i+1:]
	dir, err := os.MkdirTemp(a.stageDir(), "verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, f := range []string{"checksums.txt", "checksums.txt.sig", releaseKeyAsset} {
		if err := a.download(ctx, base+"/"+f, filepath.Join(dir, f)); err != nil {
			return fmt.Errorf("download %s: %w", f, err)
		}
	}
	sums := filepath.Join(dir, "checksums.txt")
	// Status is read from stdout alone, because stderr repeats the key's user ID and the release
	// supplies the key.
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "gpgv", "--homedir", dir, "--keyring", filepath.Join(dir, releaseKeyAsset),
		"--status-fd", "1", filepath.Join(dir, "checksums.txt.sig"), sums)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("checksums.txt signature does not verify: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if !validSigFrom(string(out), releaseKeyFingerprints) {
		return fmt.Errorf("checksums.txt is not signed by a Beacon release key (%s)", strings.Join(releaseKeyFingerprints, ", "))
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return err
	}
	return verifySHA256(pkgPath, want)
}

// validSigFrom reports whether gpgv's status output has a good signature whose primary key is one
// of fingerprints. VALIDSIG ends with the primary key's fingerprint, so a signing subkey counts.
func validSigFrom(status string, fingerprints []string) bool {
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != "[GNUPG:]" || f[1] != "VALIDSIG" {
			continue
		}
		for _, fpr := range fingerprints {
			if strings.EqualFold(f[len(f)-1], fpr) {
				return true
			}
		}
	}
	return false
}

func checksumFor(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var found []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			found = append(found, f[0])
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("signed checksums.txt has %d entries for %s, want 1", len(found), name)
	}
	return found[0], nil
}
