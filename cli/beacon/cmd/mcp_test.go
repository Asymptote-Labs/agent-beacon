package cmd

import "testing"

func TestMCPDoctorRejectsNonLoopbackHTTP(t *testing.T) {
	oldTransport := mcpOpts.transport
	oldAddr := mcpOpts.addr
	t.Cleanup(func() {
		mcpOpts.transport = oldTransport
		mcpOpts.addr = oldAddr
	})
	mcpOpts.transport = "http"
	mcpOpts.addr = "0.0.0.0:8766"
	if err := runMCPDoctor(mcpDoctorCmd, nil); err == nil {
		t.Fatal("runMCPDoctor accepted non-loopback HTTP address")
	}
}
