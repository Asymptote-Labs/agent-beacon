package updatecheck

import "testing"

func TestDisabledWhenBuildIsNotUpdatedFromPublicReleases(t *testing.T) {
	t.Cleanup(func() { PublicReleases = true })
	getenv := func(string) string { return "" }

	if Disabled(getenv) {
		t.Fatal("Disabled() with no environment = true, want false")
	}
	PublicReleases = false
	if !Disabled(getenv) {
		t.Fatal("Disabled() = false for a build that is not updated from public releases")
	}
}
