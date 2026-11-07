package selfupdate

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testdata/signed-release is a release signed by a throwaway test key with this fingerprint.
const testReleaseKeyFingerprint = "492264C703AE4B306CF804C9E0F25BE9BE03411C"

// TestVerifyReleaseSignature runs once per supported verifier, each alone, so both gpgv and the
// sqv fallback are checked wherever they are installed.
func TestVerifyReleaseSignature(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release signatures are verified on Linux only")
	}
	for _, verifier := range []string{"gpgv", "sqv"} {
		t.Run(verifier, func(t *testing.T) {
			if _, err := exec.LookPath(verifier); err != nil {
				t.Skipf("%s is not installed", verifier)
			}
			old := signatureVerifiers
			signatureVerifiers = []string{verifier}
			t.Cleanup(func() { signatureVerifiers = old })
			testVerifyReleaseSignature(t)
		})
	}
}

// A host with neither verifier gets an error that names what to install, not an exec failure.
func TestVerifyReleaseSignatureWithoutVerifier(t *testing.T) {
	old := signatureVerifiers
	signatureVerifiers = []string{"beacon-test-no-such-verifier"}
	t.Cleanup(func() { signatureVerifiers = old })
	_, err := goodSignatures(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "needs gpgv or sqv") {
		t.Fatalf("goodSignatures error = %v, want one naming gpgv or sqv", err)
	}
}

func testVerifyReleaseSignature(t *testing.T) {
	tampered := false
	// staleChecksums makes the next N checksums.txt downloads come from a different upload than
	// the signature, as a client sees while finalize replaces the pair.
	staleChecksums := 0
	checksumFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join("testdata", "signed-release", filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			checksumFetches++
			stale := staleChecksums > 0
			if stale {
				staleChecksums--
			}
			if tampered || stale {
				data = []byte(strings.Replace(string(data), "ffff", "eeee", 1))
			}
		}
		w.Write(data)
	}))
	defer srv.Close()

	old := releaseKeyFingerprints
	releaseKeyFingerprints = []string{testReleaseKeyFingerprint}
	defer func() { releaseKeyFingerprints = old }()
	oldDelay := releaseSignatureRetryDelay
	releaseSignatureRetryDelay = 0
	defer func() { releaseSignatureRetryDelay = oldDelay }()
	verify := func() error {
		a := NewApplier("1.0.0")
		a.StageDir = t.TempDir()
		pkg := filepath.Join(t.TempDir(), "download.deb")
		if err := os.WriteFile(pkg, []byte("package\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return a.verifyReleaseSignature(context.Background(), srv.URL+"/v1.0.0/beacon_1.0.0_linux_amd64.deb", pkg)
	}

	if err := verify(); err != nil {
		t.Fatalf("validly signed package refused: %v", err)
	}

	// One mismatched fetch, as during finalize's swap: the second fetch gets a matching pair.
	staleChecksums, checksumFetches = 1, 0
	if err := verify(); err != nil {
		t.Fatalf("package refused after a mismatched pair was fetched once: %v", err)
	}
	if checksumFetches != 2 {
		t.Fatalf("checksums.txt fetched %d times, want 2", checksumFetches)
	}
	// Mismatched on both fetches: refused, with no third attempt.
	staleChecksums, checksumFetches = 2, 0
	if err := verify(); err == nil {
		t.Fatal("mismatched pair accepted")
	}
	if checksumFetches != 2 {
		t.Fatalf("checksums.txt fetched %d times, want 2", checksumFetches)
	}

	tampered = true
	if err := verify(); err == nil {
		t.Fatal("tampered checksums.txt accepted")
	}
}

// withoutReleaseKeyPin clears the embedded pin for a test that exercises another part of Apply
// against a test server that serves no signed checksums.txt.
func withoutReleaseKeyPin(t *testing.T) {
	t.Helper()
	old := releaseKeyFingerprints
	releaseKeyFingerprints = nil
	t.Cleanup(func() { releaseKeyFingerprints = old })
}

// The pin is Beacon's release key, and the docs and the apt repository's committed key name the
// same one, so none of them can be changed alone.
func TestReleaseKeyPinMatchesPublishedKey(t *testing.T) {
	const want = "07BEA34ED6A411F4C066D98322F94195DCDC8F45"
	if len(releaseKeyFingerprints) != 1 || releaseKeyFingerprints[0] != want {
		t.Fatalf("release-key.fingerprint pins %v, want [%s]", releaseKeyFingerprints, want)
	}
	repo := filepath.Join("..", "..", "..", "..", "..")
	docs, err := os.ReadFile(filepath.Join(repo, "docs", "platforms", "linux.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docs), want) {
		t.Errorf("docs/platforms/linux.mdx does not publish the pinned fingerprint %s", want)
	}
	armored, err := os.ReadFile(filepath.Join(repo, "packaging", "linux", "apt", "beacon-archive-keyring.asc"))
	if err != nil {
		t.Fatal(err)
	}
	primary, err := primaryKeyFingerprints(armored)
	if err != nil {
		t.Fatalf("beacon-archive-keyring.asc: %v", err)
	}
	if len(primary) != 1 || primary[0] != want {
		t.Errorf("beacon-archive-keyring.asc holds %v, want [%s]", primary, want)
	}
}

// primaryKeyFingerprints returns the v4 fingerprint of every primary public key in an armored
// OpenPGP key block: SHA-1 over 0x99, the two-byte body length, and the public-key packet body
// (RFC 4880 section 12.2). It is computed here rather than with gpg, whose Windows build on the
// CI runners does not accept a native temporary --homedir.
func primaryKeyFingerprints(armored []byte) ([]string, error) {
	text := strings.ReplaceAll(string(armored), "\r\n", "\n")
	begin := strings.Index(text, "-----BEGIN PGP PUBLIC KEY BLOCK-----")
	end := strings.Index(text, "-----END PGP PUBLIC KEY BLOCK-----")
	if begin < 0 || end < begin {
		return nil, fmt.Errorf("no armored public key block")
	}
	body := text[begin:end]
	blank := strings.Index(body, "\n\n")
	if blank < 0 {
		return nil, fmt.Errorf("armor has no blank line after its headers")
	}
	var b64 strings.Builder
	for _, line := range strings.Split(body[blank+2:], "\n") {
		if strings.HasPrefix(line, "=") {
			break // CRC-24 checksum line
		}
		b64.WriteString(strings.TrimSpace(line))
	}
	data, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		return nil, err
	}
	var fprs []string
	for len(data) > 0 {
		hdr := data[0]
		if hdr&0x80 == 0 {
			return nil, fmt.Errorf("invalid packet header %#x", hdr)
		}
		var tag, n, hlen int
		if hdr&0x40 == 0 { // old format
			tag = int(hdr>>2) & 0x0f
			switch hdr & 0x03 {
			case 0:
				hlen, n = 2, int(data[1])
			case 1:
				hlen, n = 3, int(binary.BigEndian.Uint16(data[1:3]))
			case 2:
				hlen, n = 5, int(binary.BigEndian.Uint32(data[1:5]))
			default:
				return nil, fmt.Errorf("indeterminate packet length")
			}
		} else { // new format
			tag = int(hdr & 0x3f)
			switch o := data[1]; {
			case o < 192:
				hlen, n = 2, int(o)
			case o < 224:
				hlen, n = 3, (int(o)-192)<<8+int(data[2])+192
			case o == 255:
				hlen, n = 6, int(binary.BigEndian.Uint32(data[2:6]))
			default:
				return nil, fmt.Errorf("partial packet lengths are not used in key blocks")
			}
		}
		if hlen+n > len(data) {
			return nil, fmt.Errorf("truncated packet")
		}
		packet := data[hlen : hlen+n]
		if tag == 6 { // public-key packet (subkeys are tag 14)
			if len(packet) == 0 || packet[0] != 4 {
				return nil, fmt.Errorf("primary key is not a v4 key")
			}
			h := sha1.New()
			h.Write([]byte{0x99, byte(n >> 8), byte(n)})
			h.Write(packet)
			fprs = append(fprs, strings.ToUpper(hex.EncodeToString(h.Sum(nil))))
		}
		data = data[hlen+n:]
	}
	return fprs, nil
}

// With the real pin, a Linux update refuses a release that has no signed checksums.txt and never
// runs the package installer.
func TestApplyRefusesUnsignedReleaseOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release signatures are verified on Linux only")
	}
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := nativeManifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := NewApplier("0.0.1")
	a.ManifestURL = srv.URL + "/manifest.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "system.jsonl")
	var installed []string
	a.run = func(_ context.Context, name string, _ ...string) (string, error) {
		installed = append(installed, filepath.Base(name))
		return "", nil
	}
	_, err := a.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verify release signature") {
		t.Fatalf("Apply error = %v, want a release signature failure", err)
	}
	for _, name := range installed {
		if name == "dpkg" || name == "rpm" {
			t.Fatalf("ran %s before the signature check passed", name)
		}
	}
}
