package hermessession

import (
	"os"
	"testing"
)

func TestHermesExpansionPreservesUnsetReference(t *testing.T) {
	t.Setenv("BEACON_UNSET_FIXTURE", "")
	if err := os.Unsetenv("BEACON_UNSET_FIXTURE"); err != nil {
		t.Fatal(err)
	}
	if got := hermesExpandVars("$BEACON_UNSET_FIXTURE/work"); got != "$BEACON_UNSET_FIXTURE/work" {
		t.Fatalf("unset expansion=%q", got)
	}
}
