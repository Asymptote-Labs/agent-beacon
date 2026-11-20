package selfupdate

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// maxSignedChecksumsBytes bounds what openPGPSignatures reads of each file it checks. download
// already caps them at maxPackageBytes; checksums.txt, its signature and a public key are a few KB.
const maxSignedChecksumsBytes = 4 << 20

// isWindowsArtifact reports whether name is a Windows release artifact: the MSI a system install
// runs, or the zip a user-mode install takes beacon.exe from.
func isWindowsArtifact(name string) bool {
	return strings.HasSuffix(name, ".msi") || strings.HasSuffix(name, ".zip")
}

// openPGPSignatures is goodSignatures without gpgv or sqv, which Windows does not have: it checks
// dir/checksums.txt.sig over dir/checksums.txt in this process (ProtonMail's go-crypto) and returns
// the primary-key fingerprint of the signature's key.
//
// The release supplies dir/beacon-release-key.gpg, so only the keys in it whose primary fingerprint
// is pinned are offered to the check: a signature by any other key in that file is never even
// considered, and the key is trusted for nothing but carrying a pinned fingerprint, as with gpgv.
// A signing subkey of a pinned key counts, as it does for gpgv, when the key binds it. A signature
// that does not verify is a *signatureMismatchError, the one failure a second fetch can fix.
func openPGPSignatures(dir string, pinned []string) ([]string, error) {
	read := func(name string) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if len(data) > maxSignedChecksumsBytes {
			return nil, fmt.Errorf("%s is larger than %d bytes", name, maxSignedChecksumsBytes)
		}
		return data, nil
	}
	sums, err := read("checksums.txt")
	if err != nil {
		return nil, err
	}
	sig, err := read("checksums.txt.sig")
	if err != nil {
		return nil, err
	}
	keyData, err := read(releaseKeyAsset)
	if err != nil {
		return nil, err
	}

	var keys openpgp.EntityList
	if armored(keyData) {
		keys, err = openpgp.ReadArmoredKeyRing(bytes.NewReader(keyData))
	} else {
		keys, err = openpgp.ReadKeyRing(bytes.NewReader(keyData))
	}
	if err != nil {
		return nil, fmt.Errorf("reading the release key: %w", err)
	}
	var trusted openpgp.EntityList
	for _, key := range keys {
		if key.PrimaryKey != nil && signedByPinnedKey([]string{fingerprint(key)}, pinned) {
			trusted = append(trusted, key)
		}
	}
	if len(trusted) == 0 {
		return nil, fmt.Errorf("the release's key is not a Beacon release key (%s)", strings.Join(pinned, ", "))
	}

	var signer *openpgp.Entity
	if armored(sig) {
		signer, err = openpgp.CheckArmoredDetachedSignature(trusted, bytes.NewReader(sums), bytes.NewReader(sig), nil)
	} else {
		signer, err = openpgp.CheckDetachedSignature(trusted, bytes.NewReader(sums), bytes.NewReader(sig), nil)
	}
	if err != nil {
		return nil, &signatureMismatchError{msg: fmt.Sprintf("checksums.txt signature does not verify: %v", err)}
	}
	if signer == nil || signer.PrimaryKey == nil {
		return nil, errors.New("checksums.txt signature names no key")
	}
	return []string{fingerprint(signer)}, nil
}

func fingerprint(e *openpgp.Entity) string {
	return strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint))
}

func armored(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN PGP"))
}
