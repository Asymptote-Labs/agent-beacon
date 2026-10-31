//go:build !windows

package atomicfile

import "os"

func createSystemPrivateTemp(dir, prefix string) (*os.File, error) {
	tmp, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return nil, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	return tmp, nil
}
