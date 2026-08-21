package service

import "testing"

// TestManagedKindsCoversEveryBackend is the guard on a failure that reports success.
//
// Uninstall unloads every kind in ManagedKinds. A backend that exists but is missing from the list
// is not unloaded, so uninstall prints that it removed the endpoint while the service is still
// registered and still starts at boot -- and there is nothing in the output to suggest otherwise.
// The Windows service backend shipped that way until review caught it.
//
// Every spelling ParseKind accepts must resolve to a kind on the list, so adding a backend without
// adding it here fails rather than silently leaking.
func TestManagedKindsCoversEveryBackend(t *testing.T) {
	managed := make(map[Kind]bool, len(ManagedKinds()))
	for _, kind := range ManagedKinds() {
		if managed[kind] {
			t.Fatalf("ManagedKinds lists %q twice", kind)
		}
		managed[kind] = true
	}

	// Every accepted spelling, canonical and alias. KindAuto is excluded deliberately: it is a
	// request to detect, not a backend, and Manager resolves it to one of these before unloading.
	spellings := []string{
		"launchd",
		"systemd",
		"none", "supervised",
		"windows-service", "scm", "windows",
	}
	for _, spelling := range spellings {
		kind, err := ParseKind(spelling)
		if err != nil {
			t.Fatalf("ParseKind(%q): %v", spelling, err)
		}
		if !managed[kind] {
			t.Fatalf("ParseKind(%q) = %q, which ManagedKinds does not list; "+
				"an endpoint installed under it would survive uninstall", spelling, kind)
		}
	}

	// Each listed kind must resolve to a backend that agrees it is that kind. A typo in the list
	// would otherwise unload nothing and still look right.
	for _, kind := range ManagedKinds() {
		for _, userMode := range []bool{false, true} {
			got := (Manager{UserMode: userMode, Kind: kind}).backend()
			if got == nil {
				t.Fatalf("kind %q (userMode=%v) resolves to no backend", kind, userMode)
			}
			// Windows user mode intentionally routes to supervised: there is no per-user service
			// manager to route it to. Every other pairing must round-trip.
			if got.kind() != kind && !(kind == KindWindowsService && userMode) {
				t.Fatalf("kind %q (userMode=%v) resolves to backend %q", kind, userMode, got.kind())
			}
		}
	}
}
