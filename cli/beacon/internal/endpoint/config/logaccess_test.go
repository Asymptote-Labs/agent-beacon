package config

import (
	"runtime"
	"testing"
)

func TestRestrictedLogAccess(t *testing.T) {
	cases := []struct {
		cfg  Config
		want bool
	}{
		{Config{}, false},
		{Config{LogAccess: "shared"}, false},
		{Config{LogAccess: "restricted"}, runtime.GOOS != "windows"},
		{Config{LogAccess: " Restricted "}, runtime.GOOS != "windows"},
		{Config{UserMode: true, LogAccess: "restricted"}, false},
		{Config{LogAccess: "restricted-ish"}, false},
	}
	for _, c := range cases {
		if got := c.cfg.RestrictedLogAccess(); got != c.want {
			t.Errorf("%+v.RestrictedLogAccess() = %v, want %v", c.cfg, got, c.want)
		}
	}
}
