//go:build windows

package cmd

import "errors"

// packagesAsUser is never reached on Windows, where the inventory scan is never confined
// (confineInventoryScan confines only a root process).
func packagesAsUser(int, string) (packageResolver, error) {
	return nil, errors.New("resolving packages as another account is not supported on Windows")
}
