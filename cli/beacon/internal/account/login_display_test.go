package account

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Over SSH, or on a machine with no display, xdg-open starts and then fails, and the start was
// all Login used to check. It printed "Opening https://beacon.sh in your browser...", showed no
// URL, and waited out the whole timeout. It has to say why at once and show the URL and the port
// to forward.
func TestLoginWithoutADisplayShowsTheURLAtOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display check is Linux-only, and the real opener elsewhere would open a browser")
	}
	service := newFakeAuthService(t)

	// An xdg-open that "succeeds", the way the real one does when it has nowhere to go.
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "xdg-open-ran")
	script := "#!/bin/sh\ntouch '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "xdg-open"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("SSH_CONNECTION", "10.0.0.5 51000 10.0.0.9 22")

	var out bytes.Buffer
	_, err := Login(context.Background(), LoginOptions{
		BaseURL: service.server.URL,
		Out:     &out,
		Timeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("nothing approved the sign-in, so Login should have timed out")
	}
	// Given a moment in case the opener was started asynchronously.
	time.Sleep(100 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("xdg-open ran in an SSH session with no display")
	}
	text := out.String()
	for _, want := range []string{
		"this is an SSH session",
		"redirects a browser back to this machine",
		service.server.URL + LoginPagePath + "?",
		"ssh -L ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("login output is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "in your browser...") {
		t.Fatalf("login claimed a browser was opening:\n%s", text)
	}
}
