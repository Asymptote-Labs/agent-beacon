package sqliteuri

import "testing"

func TestFileURLKeepsAWindowsDriveLetterOutOfTheAuthority(t *testing.T) {
	// The inputs are already slash-separated, which is what filepath.ToSlash hands fileURL, so the
	// Windows shapes are exercised on every platform rather than only on the Windows runner.
	for _, tc := range []struct {
		name, slashed, want string
	}{
		{"windows drive", "C:/Users/me/AppData/Roaming/opencode/opencode.db", "file:///C:/Users/me/AppData/Roaming/opencode/opencode.db"},
		{"posix", "/home/me/.config/opencode/opencode.db", "file:///home/me/.config/opencode/opencode.db"},
		{"unc share", "//server/share/opencode/opencode.db", "file://server/share/opencode/opencode.db"},
		{"uri delimiters", "/home/me/C#/100%/a?b/x.db", "file:///home/me/C%23/100%25/a%3Fb/x.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := fileURL(tc.slashed)
			if got := u.String(); got != tc.want {
				t.Fatalf("fileURL(%q) = %q, want %q", tc.slashed, got, tc.want)
			}
		})
	}
}
