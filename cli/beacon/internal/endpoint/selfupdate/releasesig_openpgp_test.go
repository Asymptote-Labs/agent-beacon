package selfupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// throwawayKey makes a signing key for one test.
func throwawayKey(t *testing.T, name string) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity(name, "", name+"@example.invalid", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// publicKeys is the binary public keyring of keys, as release.yml publishes beacon-release-key.gpg.
func publicKeys(t *testing.T, keys ...*openpgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, k := range keys {
		if err := k.Serialize(&buf); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// detachSign is a binary detached signature by key over data, as GoReleaser's signs entry makes.
func detachSign(t *testing.T, key *openpgp.Entity, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := openpgp.DetachSign(&buf, key, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func armorBlock(t *testing.T, kind string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func keyFingerprint(e *openpgp.Entity) string {
	return strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint))
}

// pinKeys pins fingerprints for one test.
func pinKeys(t *testing.T, fprs ...string) {
	t.Helper()
	old := releaseKeyFingerprints
	releaseKeyFingerprints = fprs
	t.Cleanup(func() { releaseKeyFingerprints = old })
}

func writeSignedChecksums(t *testing.T, sums, sig, key []byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string][]byte{"checksums.txt": sums, "checksums.txt.sig": sig, releaseKeyAsset: key} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The in-process check accepts what gpgv accepts: the release fixture gpg signed.
func TestOpenPGPSignaturesVerifiesTheSignedReleaseFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "signed-release")
	signers, err := openPGPSignatures(fixture, []string{testReleaseKeyFingerprint})
	if err != nil {
		t.Fatalf("openPGPSignatures: %v", err)
	}
	if len(signers) != 1 || signers[0] != testReleaseKeyFingerprint {
		t.Fatalf("signers = %v", signers)
	}

	// The same files with another key pinned: the release's key is trusted for nothing else.
	if _, err := openPGPSignatures(fixture, []string{"07BEA34ED6A411F4C066D98322F94195DCDC8F45"}); err == nil ||
		!strings.Contains(err.Error(), "not a Beacon release key") {
		t.Fatalf("unpinned key: err = %v", err)
	}

	// An edited checksums.txt is a signature that does not verify, which a second fetch may fix.
	sums := mustRead(t, filepath.Join(fixture, "checksums.txt"))
	dir := writeSignedChecksums(t, bytes.Replace(sums, []byte("ffff"), []byte("eeee"), 1),
		mustRead(t, filepath.Join(fixture, "checksums.txt.sig")), mustRead(t, filepath.Join(fixture, releaseKeyAsset)))
	var mismatch *signatureMismatchError
	if _, err := openPGPSignatures(dir, []string{testReleaseKeyFingerprint}); !errors.As(err, &mismatch) {
		t.Fatalf("tampered checksums.txt: err = %v, want a signature mismatch", err)
	}
}

func TestOpenPGPSignaturesTrustsOnlyPinnedKeys(t *testing.T) {
	pinned, other := throwawayKey(t, "pinned"), throwawayKey(t, "other")
	sums := []byte(strings.Repeat("a", 64) + "  beacon-enterprise_1.3.41_windows_amd64.msi\n")
	pins := []string{keyFingerprint(pinned)}

	cases := map[string]struct {
		sig, key []byte
		ok       bool
	}{
		"signed by the pinned key":         {detachSign(t, pinned, sums), publicKeys(t, pinned), true},
		"armored":                          {armorBlock(t, "PGP SIGNATURE", detachSign(t, pinned, sums)), armorBlock(t, "PGP PUBLIC KEY BLOCK", publicKeys(t, pinned)), true},
		"signed by a key beside the pin":   {detachSign(t, other, sums), publicKeys(t, pinned, other), false},
		"signed by an unpinned key alone":  {detachSign(t, other, sums), publicKeys(t, other), false},
		"pinned key over other bytes":      {detachSign(t, pinned, []byte("something else")), publicKeys(t, pinned), false},
		"not a signature":                  {[]byte("garbage"), publicKeys(t, pinned), false},
		"the release's key file is broken": {detachSign(t, pinned, sums), []byte("garbage"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			signers, err := openPGPSignatures(writeSignedChecksums(t, sums, tc.sig, tc.key), pins)
			if tc.ok {
				if err != nil || !signedByPinnedKey(signers, pins) {
					t.Fatalf("signers %v, err %v", signers, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, signers %v", signers)
			}
		})
	}
}

// Beacon's release key is RSA (the fixtures and throwaway keys are EdDSA).
func TestOpenPGPSignaturesAcceptsAnRSAKey(t *testing.T) {
	key, err := openpgp.NewEntity("rsa", "", "rsa@example.invalid", &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048})
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte(strings.Repeat("b", 64) + "  BeaconEndpointAgent-1.3.40-x64.msi\n")
	signers, err := openPGPSignatures(writeSignedChecksums(t, sums, detachSign(t, key, sums), publicKeys(t, key)), []string{keyFingerprint(key)})
	if err != nil || len(signers) != 1 || signers[0] != keyFingerprint(key) {
		t.Fatalf("signers %v, err %v", signers, err)
	}
}

// signedReleaseServer serves files and a checksums.txt listing them, signed by key, as a release
// does, with the signature and the public key beside it.
func signedReleaseServer(t *testing.T, key *openpgp.Entity, files map[string][]byte) *httptest.Server {
	t.Helper()
	var sums strings.Builder
	for name, data := range files {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	served := map[string][]byte{
		"checksums.txt":     []byte(sums.String()),
		"checksums.txt.sig": detachSign(t, key, []byte(sums.String())),
		releaseKeyAsset:     publicKeys(t, key),
	}
	for name, data := range files {
		served[name] = data
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := served[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The user-mode Windows install fetches the release zip and checks it in this process, on any OS:
// gpgv is never asked.
func TestFetchVerifiedPackageChecksAWindowsZipInProcess(t *testing.T) {
	old := signatureVerifiers
	signatureVerifiers = []string{"beacon-test-no-such-verifier"}
	t.Cleanup(func() { signatureVerifiers = old })
	key := throwawayKey(t, "release")
	pinKeys(t, keyFingerprint(key))
	zipBytes := []byte("zip bytes")
	const name = "beacon-enterprise_1.3.41_windows_amd64.zip"
	srv := signedReleaseServer(t, key, map[string][]byte{name: zipBytes})

	pkg := Package{
		URL: srv.URL + "/" + name, SHA256: sha256Hex(zipBytes), Version: "1.3.41", Name: name,
		Checksums: SignedChecksums{URL: srv.URL + "/checksums.txt", SignatureURL: srv.URL + "/checksums.txt.sig", KeyURL: srv.URL + "/" + releaseKeyAsset},
	}
	a := NewApplier("")
	if err := a.FetchVerifiedPackage(t.Context(), pkg, filepath.Join(t.TempDir(), "enterprise.zip")); err != nil {
		t.Fatalf("FetchVerifiedPackage: %v", err)
	}

	// Pinned to another key: refused, though the bytes match the checksum it was offered with.
	pinKeys(t, keyFingerprint(throwawayKey(t, "someone else")))
	if err := a.FetchVerifiedPackage(t.Context(), pkg, filepath.Join(t.TempDir(), "enterprise.zip")); err == nil ||
		!strings.Contains(err.Error(), "verify release signature") {
		t.Fatalf("err = %v, want the signed checksums to refuse it", err)
	}

	// A build that pins nothing refuses a Windows package outright, where Linux would fall back to
	// the checksum alone.
	pinKeys(t)
	if err := a.FetchVerifiedPackage(t.Context(), pkg, filepath.Join(t.TempDir(), "enterprise.zip")); err == nil ||
		!strings.Contains(err.Error(), "pins no Beacon release key") {
		t.Fatalf("err = %v, want a refusal without a pinned key", err)
	}
}
