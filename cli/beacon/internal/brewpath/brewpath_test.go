package brewpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStableMapsAKegPathToItsLink(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "beacon", "1.3.20", "bin", "beacon-vector")
	linked := filepath.Join(prefix, "bin", "beacon-vector")

	// Without the link there is nothing better to return.
	if got := Stable(keg); got != keg {
		t.Fatalf("Stable(keg) with no link = %q, want %q", got, keg)
	}
	if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linked, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Stable(keg); got != linked {
		t.Fatalf("Stable(keg) = %q, want the link %q", got, linked)
	}
	if got := Stable("/opt/beacon/bin/vector"); got != "/opt/beacon/bin/vector" {
		t.Fatalf("a path outside a Cellar changed: %q", got)
	}
}
