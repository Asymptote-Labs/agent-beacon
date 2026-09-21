//go:build !windows

package account

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
