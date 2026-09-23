package cmd

import "testing"

// #447: the health-check port is settable per install, like the OTLP ports, and defaults to 0 so
// the lifecycle derives it rather than every install getting a fixed 13133.
func TestEndpointInstallAndRepairExposeHealthPortFlag(t *testing.T) {
	for _, cmd := range []string{"install", "repair"} {
		c := endpointInstallCmd
		if cmd == "repair" {
			c = endpointRepairCmd
		}
		flag := c.Flags().Lookup("health-port")
		if flag == nil {
			t.Fatalf("endpoint %s has no --health-port flag", cmd)
		}
		if flag.DefValue != "0" {
			t.Fatalf("endpoint %s --health-port default = %q, want 0 (derive from the OTLP ports)", cmd, flag.DefValue)
		}
	}
}
