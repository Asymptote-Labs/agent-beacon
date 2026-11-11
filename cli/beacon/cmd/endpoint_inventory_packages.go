package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
)

// packageResolver answers which package each MCP server runs; see endpointinventory.Options.
type packageResolver = func(launchpkg.RegistryEnv, []launchpkg.Query) ([]launchpkg.Answer, error)

// packageRequest is what the system inventory job asks resolve-packages.
type packageRequest struct {
	Env     launchpkg.RegistryEnv `json:"env"`
	Queries []launchpkg.Query     `json:"queries"`
}

const (
	// maxPackageExchangeBytes bounds a request or an answer; a snapshot's servers fit many times.
	maxPackageExchangeBytes = 16 << 20
	// packageResolveTimeout bounds the console user's resolver. A FIFO is opened non-blocking, but
	// a hung network home is not, and the job holds the inventory lock while it waits.
	packageResolveTimeout = 30 * time.Second
	// packageResolveWaitDelay is how long the job waits, once the timeout has killed the child, for
	// its pipes to close. The child is the console user's own process, so they can hold its stdout
	// open from another process (/proc/<pid>/fd/1, or a child it started); without a bound the job
	// would wait for them forever while holding the inventory lock, and no heartbeat would go out.
	packageResolveWaitDelay = 2 * time.Second
)

// endpointInventoryResolvePackagesCmd names the package each MCP server runs, reading the launcher
// config those servers' launchers read. The system inventory job runs it as the console user (see
// packagesAsUser), so that config, wherever the user pointed it, is read with their rights and
// never root's: whatever they could not read, Beacon cannot either, and holds back as private. It
// reads a packageRequest as JSON on stdin and writes the answers as JSON on stdout.
var endpointInventoryResolvePackagesCmd = &cobra.Command{
	Use:          "resolve-packages",
	Short:        "Internal: name MCP server packages with the current user's launcher config",
	Hidden:       true,
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		var req packageRequest
		if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), maxPackageExchangeBytes)).Decode(&req); err != nil {
			return fmt.Errorf("read package request: %w", err)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(launchpkg.ResolveAll(req.Env, req.Queries))
	},
}

func init() {
	endpointInventoryCmd.AddCommand(endpointInventoryResolvePackagesCmd)
}

// packageResolverCommand is the resolver child; a var so tests can run it in the test binary.
var packageResolverCommand = func(ctx context.Context, exe string) *exec.Cmd {
	return exec.CommandContext(ctx, exe, "endpoint", "inventory", "resolve-packages")
}

// resolvePackagesWith runs the resolver child c and reads its answers. The child runs as the
// console user, who may tamper with it, so its answer is bounded and checked like any input: it
// can only mislabel that user's own servers, which their own config already decides.
func resolvePackagesWith(c *exec.Cmd, env launchpkg.RegistryEnv, queries []launchpkg.Query) ([]launchpkg.Answer, error) {
	in, err := json.Marshal(packageRequest{Env: env, Queries: queries})
	if err != nil {
		return nil, err
	}
	var out, stderr bytes.Buffer
	c.Stdin = bytes.NewReader(in)
	c.Stdout = &cappedBuffer{buf: &out, limit: maxPackageExchangeBytes}
	c.Stderr = &cappedBuffer{buf: &stderr, limit: 4 << 10}
	c.WaitDelay = packageResolveWaitDelay
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("resolve MCP server packages as the console user: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	var answers []launchpkg.Answer
	if err := json.Unmarshal(out.Bytes(), &answers); err != nil {
		return nil, fmt.Errorf("read MCP server packages: %w", err)
	}
	if len(answers) != len(queries) {
		return nil, fmt.Errorf("read MCP server packages: %d answers for %d servers", len(answers), len(queries))
	}
	return answers, nil
}

var errExchangeTooLarge = errors.New("resolver output over the size limit")

// cappedBuffer fails a write that would take buf past limit.
type cappedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		return 0, errExchangeTooLarge
	}
	return w.buf.Write(p)
}
