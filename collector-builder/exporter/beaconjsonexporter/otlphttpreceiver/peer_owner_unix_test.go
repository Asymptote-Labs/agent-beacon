//go:build linux || darwin

package otlphttpreceiver

import (
	"os"
	"strconv"
	"testing"
)

// currentOwner is how peercred names this process's account: its effective uid.
func currentOwner(t *testing.T) string {
	t.Helper()
	return strconv.Itoa(os.Geteuid())
}
