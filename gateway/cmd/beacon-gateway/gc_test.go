package main

import "testing"

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"6GiB": 6 << 30, "512MiB": 512 << 20, "1024": 1024, "2KiB": 2048, "7B": 7} {
		if got, err := parseBytes(in); err != nil || got != want {
			t.Errorf("parseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "6GB", "-1GiB", "x", "9999999999TiB"} {
		if _, err := parseBytes(in); err == nil {
			t.Errorf("parseBytes(%q) accepted", in)
		}
	}
}
