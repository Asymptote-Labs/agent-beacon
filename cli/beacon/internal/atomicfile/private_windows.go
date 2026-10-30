//go:build windows

package atomicfile

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

// createSystemPrivateTemp is os.CreateTemp with the file created private (sysacl.CreatePrivateFile),
// so there is no moment in which it carries the directory's inherited grants.
func createSystemPrivateTemp(dir, prefix string) (*os.File, error) {
	for range 10000 {
		name := filepath.Join(dir, prefix+strconv.FormatUint(uint64(rand.Uint32()), 10))
		f, err := sysacl.CreatePrivateFile(name)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, &fs.PathError{Op: "createtemp", Path: filepath.Join(dir, prefix+"*"), Err: fs.ErrExist}
}
