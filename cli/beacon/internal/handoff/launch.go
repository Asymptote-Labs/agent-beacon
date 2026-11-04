package handoff

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
)

// Launch runs the plan's runtime in the current terminal and waits for it, returning its exit
// code. The runtime owns the terminal while it runs: an interrupt reaches it through the terminal's
// process group, so Beacon ignores the interrupt itself instead of exiting underneath it.
func Launch(plan Plan, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return launch(plan, stdin, stdout, stderr, nil)
}

// LaunchWithStarted runs the plan like Launch and calls started after the runtime process has
// started successfully. It is used by the detached-launch trampoline to tell the parent Beacon
// process that the new terminal is ready.
func LaunchWithStarted(plan Plan, stdin io.Reader, stdout, stderr io.Writer, started func() error) (int, error) {
	return launch(plan, stdin, stdout, stderr, started)
}

func launch(plan Plan, stdin io.Reader, stdout, stderr io.Writer, started func() error) (int, error) {
	cmd := exec.Command(plan.Executable, plan.Args...)
	cmd.Dir = plan.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = launchEnv(os.Environ(), plan.Env)

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	if err := cmd.Start(); err != nil {
		return -1, err
	}
	if started != nil {
		if err := started(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return -1, err
		}
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// launchEnv applies the plan's overrides to environ. A name is matched as the platform matches it:
// case-insensitively on Windows.
func launchEnv(environ []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return environ
	}
	env := make([]string, 0, len(environ)+len(overrides))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !overridden(name, overrides) {
			env = append(env, entry)
		}
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value := overrides[name]; value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func overridden(name string, overrides map[string]string) bool {
	for candidate := range overrides {
		if envNameEqual(name, candidate) {
			return true
		}
	}
	return false
}
