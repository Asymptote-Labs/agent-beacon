package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMain puts stand-ins for the service managers first on PATH for the whole package.
//
// Uninstall and repair unload every backend speculatively (service.ManagedKinds), and the names
// they unload belong to the machine, not the test. A temp HOME moves the plist and unit files but
// not gui/<uid>/com.beacon.endpoint.collector.user or beacon-collector.service. Run against the
// real launchctl, these tests booted out the developer's own collector and inventory agent.
//
// The stand-ins answer as a machine with nothing installed, which is what CI sees, and refuse
// anything that would register or start a service.
func TestMain(m *testing.M) {
	os.Exit(runWithServiceManagerStandIns(m))
}

func runWithServiceManagerStandIns(m *testing.M) int {
	// Windows has no launchctl or systemctl to reach, and user mode there is the supervised
	// backend, whose state lives under HOME.
	if runtime.GOOS == "windows" {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "beacon-lifecycle-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "service manager stand-ins:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	for name, script := range serviceManagerStandIns {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "service manager stand-ins:", err)
			return 1
		}
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		fmt.Fprintln(os.Stderr, "service manager stand-ins:", err)
		return 1
	}
	return m.Run()
}

// Each stand-in prints what the real tool prints for an absent job or unit, in the form the
// service package parses (launchctlNoSuchProcess, systemdUnitMissing).
var serviceManagerStandIns = map[string]string{
	"launchctl": `#!/bin/sh
case "$1" in
print)
	echo "Could not find service \"${2##*/}\" in domain for port"
	exit 113
	;;
bootout)
	echo "Boot-out failed: 3: No such process"
	exit 3
	;;
esac
echo "lifecycle tests do not run launchctl $*" >&2
exit 1
`,
	"systemctl": `#!/bin/sh
verb=
for arg in "$@"; do
	case "$arg" in
	-*) ;;
	*) verb=$arg; break ;;
	esac
done
for unit in "$@"; do :; done
case "$verb" in
stop | disable | restart)
	echo "Failed to $verb $unit: Unit $unit not loaded." >&2
	exit 5
	;;
is-enabled)
	echo "Failed to get unit file state for $unit: No such file or directory" >&2
	exit 1
	;;
is-active)
	echo inactive
	exit 3
	;;
daemon-reload)
	exit 0
	;;
esac
echo "lifecycle tests do not run systemctl $*" >&2
exit 1
`,
	"loginctl": `#!/bin/sh
if [ "$1" = show-user ]; then
	echo no
	exit 0
fi
echo "lifecycle tests do not run loginctl $*" >&2
exit 1
`,
}
