package handoff

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
)

// Launch runs the plan's runtime in the current terminal and waits for it, returning its exit
// code. The runtime owns the terminal while it runs: an interrupt reaches it through the terminal's
// process group, so Beacon ignores the interrupt itself instead of exiting underneath it.
func Launch(plan Plan, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(plan.Executable, plan.Args...)
	cmd.Dir = plan.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = os.Environ()

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
