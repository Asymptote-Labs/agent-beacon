package intel

import (
	"errors"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

func TestNewReadsTheEnvironment(t *testing.T) {
	t.Setenv(EnvDisabled, "")
	t.Setenv(EnvURL, "")
	c, err := New("beacon/test")
	if err != nil || c.BaseURL != intelapi.DefaultBaseURL || c.UserAgent != "beacon/test" || c.HTTPClient == nil {
		t.Fatalf("New() = %+v, %v", c, err)
	}
	t.Setenv(EnvURL, "http://127.0.0.1:9/")
	if c, err = New(""); err != nil || c.BaseURL != "http://127.0.0.1:9" {
		t.Fatalf("New() with %s = %+v, %v", EnvURL, c, err)
	}
	t.Setenv(EnvURL, "http://intel.example.test")
	if _, err = New(""); err == nil || !strings.Contains(err.Error(), EnvURL) {
		t.Fatalf("New() with a plain-http remote host = %v, want an error naming %s", err, EnvURL)
	}
	for _, off := range []string{"0", "false", "OFF", " no "} {
		t.Setenv(EnvDisabled, off)
		if _, err := New(""); !errors.Is(err, ErrDisabled) {
			t.Fatalf("New() with %s=%q = %v, want ErrDisabled", EnvDisabled, off, err)
		}
	}
	t.Setenv(EnvDisabled, "1")
	t.Setenv(EnvURL, "")
	if _, err := New(""); err != nil {
		t.Fatalf("New() with %s=1 = %v", EnvDisabled, err)
	}
}
