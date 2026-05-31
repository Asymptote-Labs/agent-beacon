package updatecheck

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
		wantOK  bool
	}{
		{name: "latest newer", current: "v0.0.10", latest: "v0.0.12", want: -1, wantOK: true},
		{name: "equal with optional v", current: "0.0.12", latest: "v0.0.12", want: 0, wantOK: true},
		{name: "current newer", current: "v0.1.0", latest: "v0.0.12", want: 1, wantOK: true},
		{name: "prerelease unsupported", current: "v0.0.10", latest: "v0.0.12-beta.1", wantOK: false},
		{name: "dev unsupported", current: "dev", latest: "v0.0.12", wantOK: false},
		{name: "unparsable unsupported", current: "v0.0.10", latest: "latest", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := compareVersions(tt.current, tt.latest)
			if ok != tt.wantOK {
				t.Fatalf("compareVersions ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("compareVersions = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCanCheckVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "v0.0.12", want: true},
		{version: "0.0.12", want: true},
		{version: "dev", want: false},
		{version: "", want: false},
		{version: "v0.0.12-beta.1", want: false},
	}
	for _, tt := range tests {
		if got := CanCheckVersion(tt.version); got != tt.want {
			t.Fatalf("CanCheckVersion(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}
