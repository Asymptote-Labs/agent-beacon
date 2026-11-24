package cmd

import (
	"bytes"
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version"
)

func TestHuntedProbe(t *testing.T) {
	// Save the original command's output writer and os.Stdout
	oldStdout := os.Stdout
	originalCmdOutput := versionCmd.OutOrStdout()

	// Create a bytes.Buffer for cmd.SetOut() to capture Cobra's intended output
	var cmdBuf bytes.Buffer
	versionCmd.SetOut(&cmdBuf) // Redirect Cobra's internal output stream

	// Create a pipe to capture os.Stdout directly, to prove the bypass
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	os.Stdout = w // Redirect os.Stdout

	// Execute the command's Run function
	// The Run function `fmt.Println(version.Version)` writes to `os.Stdout`, 
	// ignoring the output stream configured by `cmd.SetOut()`.
	versionCmd.Run(versionCmd, []string{})

	// Close the write end of the pipe and restore os.Stdout
	w.Close()
	os.Stdout = oldStdout

	// Read everything from the read end of the pipe (this captures fmt.Println output)
	capturedByOsStdout, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	r.Close()

	// Get output from cmd.SetOut() buffer
	outputFromCmdSetOut := strings.TrimSpace(cmdBuf.String())
	expectedVersion := strings.TrimSpace(version.Version)

	// The core assertion: The output should have been captured by cmd.SetOut(),
	// but it is not because fmt.Println bypasses Cobra's I/O abstraction.
	// This assertion will FAIL on the current code and PASS once fixed.
	// If the code is fixed to use `cmd.OutOrStdout()`, then `outputFromCmdSetOut`
	// will correctly contain `expectedVersion`.
	if outputFromCmdSetOut != expectedVersion {
		t.Errorf("FAIL: Framework I/O abstraction bypass detected. Expected version '%s' to be written to cmd.OutOrStdout() (captured by cmd.SetOut buffer), but received '%s'.", expectedVersion, outputFromCmdSetOut)
		t.Logf("The command directly writes to os.Stdout via fmt.Println instead of using cmd.OutOrStdout().")
		// Prove that it did go to os.Stdout directly
		capturedByActualStdout := strings.TrimSpace(string(capturedByOsStdout))
		if capturedByActualStdout != expectedVersion {
			t.Errorf("Additionally, expected version '%s' to be captured by redirecting os.Stdout, but got '%s'. This indicates a problem with the probe setup or the actual output.", expectedVersion, capturedByActualStdout)
		} else {
			t.Logf("As expected, output was captured by redirecting os.Stdout: '%s'", capturedByActualStdout)
		}
	} else {
		t.Logf("PASS: Output correctly captured by cmd.SetOut buffer: '%s'. The I/O abstraction is respected.", outputFromCmdSetOut)
	}

	// Restore the command's output writer
	versionCmd.SetOut(originalCmdOutput)
}
