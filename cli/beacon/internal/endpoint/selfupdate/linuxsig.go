package selfupdate

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// pinnedReleaseKeys holds the OpenPGP fingerprints, one per line, of the keys allowed to sign a
// release's checksums.txt; two only while a key is rotated. It pins Beacon's Linux release key, so
// a .deb or .rpm is installed only if a checksums.txt signed by one of these keys lists it, on top
// of the manifest's SHA-256. The release copy of packaging/linux/install.sh is stamped from the
// same file, so changing the pinned key is a change to release-key.fingerprint alone (published in
// docs/platforms/linux.mdx#release-signing).
//
//go:embed release-key.fingerprint
var pinnedReleaseKeys string

var releaseKeyFingerprints = strings.Fields(pinnedReleaseKeys)

// releaseKeyAsset is the release's public key, exported from the signing key by release.yml. It
// is fetched from the release like checksums.txt, so it is trusted only for carrying the pinned
// fingerprint.
const releaseKeyAsset = "beacon-release-key.gpg"

// releaseSignatureRetryDelay is how long verifyReleaseSignature waits before fetching checksums.txt
// and its signature a second time. The release's finalize job replaces both after the release is
// public, and GitHub replaces assets one at a time, so for a few seconds a client can download a
// checksums.txt and a signature from different uploads; a second fetch gets a matching pair.
var releaseSignatureRetryDelay = 5 * time.Second

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
	if err := a.fetchSignedChecksums(ctx, base, dir); err != nil {
		var mismatch *signatureMismatchError
		if !errors.As(err, &mismatch) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(releaseSignatureRetryDelay):
		}
		if err := a.fetchSignedChecksums(ctx, base, dir); err != nil {
			return err
		}
	}
	want, err := checksumFor(filepath.Join(dir, "checksums.txt"), name)
	if err != nil {
		return err
	}
	return verifySHA256(pkgPath, want)
}

// signatureMismatchError is a checksums.txt whose signature does not verify, the one failure a
// second fetch can fix.
type signatureMismatchError struct{ msg string }

func (e *signatureMismatchError) Error() string { return e.msg }

// fetchSignedChecksums downloads checksums.txt, its signature and the release key into dir and
// checks that a pinned key signed that checksums.txt.
func (a *Applier) fetchSignedChecksums(ctx context.Context, base, dir string) error {
	for _, f := range []string{"checksums.txt", "checksums.txt.sig", releaseKeyAsset} {
		if err := a.download(ctx, base+"/"+f, filepath.Join(dir, f)); err != nil {
			return fmt.Errorf("download %s: %w", f, err)
		}
	}
	signers, err := goodSignatures(ctx, dir)
	if err != nil {
		return err
	}
	if !signedByPinnedKey(signers, releaseKeyFingerprints) {
		return fmt.Errorf("checksums.txt is not signed by a Beacon release key (%s)", strings.Join(releaseKeyFingerprints, ", "))
	}
	return nil
}

// signatureVerifiers are the programs that can check checksums.txt.sig, in order of preference:
// gpgv on most systems, and sqv (Sequoia) where it replaces gpgv, as on Debian trixie-based images.
var signatureVerifiers = []string{"gpgv", "sqv"}

// goodSignatures verifies dir/checksums.txt.sig over dir/checksums.txt with the release key in dir
// and returns the primary-key fingerprint of each good signature. A signature that does not verify
// is a *signatureMismatchError, the one failure a second fetch can fix.
func goodSignatures(ctx context.Context, dir string) ([]string, error) {
	sig, sums, key := filepath.Join(dir, "checksums.txt.sig"), filepath.Join(dir, "checksums.txt"), filepath.Join(dir, releaseKeyAsset)
	for _, verifier := range signatureVerifiers {
		path, err := exec.LookPath(verifier)
		if err != nil {
			continue
		}
		var cmd *exec.Cmd
		if verifier == "gpgv" {
			// Status is read from stdout alone, because stderr repeats the key's user ID and the
			// release supplies the key.
			cmd = exec.CommandContext(ctx, path, "--homedir", dir, "--keyring", key, "--status-fd", "1", sig, sums)
		} else {
			// sqv prints the fingerprint of each good signature's key on stdout.
			cmd = exec.CommandContext(ctx, path, "--keyring", key, sig, sums)
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			msg := fmt.Sprintf("checksums.txt signature does not verify (%s): %v: %s", verifier, err, strings.TrimSpace(stderr.String()))
			if errors.As(err, &exit) {
				return nil, &signatureMismatchError{msg: msg}
			}
			return nil, errors.New(msg)
		}
		if verifier == "gpgv" {
			return validSigFingerprints(string(out)), nil
		}
		return strings.Fields(string(out)), nil
	}
	return nil, fmt.Errorf("verifying the release signature needs gpgv or sqv; install gpgv (apt-get install gpgv, dnf install gnupg2) and retry")
}

// validSigFingerprints returns the last field of each VALIDSIG line in gpgv's status output, the
// signature's primary-key fingerprint, so a signing subkey counts.
func validSigFingerprints(status string) []string {
	var fprs []string
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "[GNUPG:]" && f[1] == "VALIDSIG" {
			fprs = append(fprs, f[len(f)-1])
		}
	}
	return fprs
}

// signedByPinnedKey reports whether any good signature's fingerprint is one of the pinned ones.
func signedByPinnedKey(signers, pinned []string) bool {
	for _, s := range signers {
		for _, fpr := range pinned {
			if strings.EqualFold(s, fpr) {
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
