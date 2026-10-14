package selfupdate

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

func TestResolveModeIsOffWhenBuildIsNotUpdatedFromPublicReleases(t *testing.T) {
	origManaged := ManagedConfigPath
	ManagedConfigPath = filepath.Join(t.TempDir(), "managed.json")
	t.Cleanup(func() {
		ManagedConfigPath = origManaged
		updatecheck.PublicReleases = true
	})
	if err := writeFile(ManagedConfigPath, `{"auto_update":{"mode":"auto"}}`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AutoUpdateEnv, "auto")

	if got := ResolveMode("auto"); got != ModeAuto {
		t.Fatalf("ResolveMode() = %q, want auto", got)
	}
	updatecheck.PublicReleases = false
	if got := ResolveMode("auto"); got != ModeOff {
		t.Fatalf("ResolveMode() = %q, want off for a build that is not updated from public releases", got)
	}
}
