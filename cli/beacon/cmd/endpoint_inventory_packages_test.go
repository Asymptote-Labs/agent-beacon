package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
)

// TestPackageResolverHelperProcess is the resolver child when a test runs this binary as one. It
// is told so by its arguments: packagesAsUser gives the child an environment of its own.
func TestPackageResolverHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || len(os.Args) != i+2 {
		return
	}
	switch os.Args[i+1] {
	case "resolve-packages":
		endpointInventoryResolvePackagesCmd.SetIn(os.Stdin)
		endpointInventoryResolvePackagesCmd.SetOut(os.Stdout)
		if err := endpointInventoryResolvePackagesCmd.RunE(endpointInventoryResolvePackagesCmd, nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "too-few":
		fmt.Print("[]")
	case "too-much":
		fmt.Print(strings.Repeat(" ", maxPackageExchangeBytes+1))
	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	case "hold-stdout":
		// A process the console user controls keeps the child's stdout open after it dies.
		holder := helperResolver(context.Background(), "sleep")
		holder.Stdout = os.Stdout
		if err := holder.Start(); err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Minute)
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func helperResolver(ctx context.Context, mode string) *exec.Cmd {
	return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPackageResolverHelperProcess$", "--", mode)
}

var testQueries = []launchpkg.Query{
	{Launch: map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/billing-mcp"}}},
	{Launch: map[string]interface{}{"command": "node", "args": []interface{}{"server.js"}}},
}

// The child's answers come back in query order; an answer that does not fit the question, or a
// child that fails, is an error, which the scan turns into every package held back as private.
func TestResolvePackagesWithAChild(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(home+"/.npmrc", []byte("@acme:registry=https://npm.acme.internal/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	answers, err := resolvePackagesWith(helperResolver(context.Background(), "resolve-packages"), launchpkg.RegistryEnv{Home: home}, testQueries)
	if err != nil {
		t.Fatal(err)
	}
	want := []launchpkg.Answer{{Package: launchpkg.Package{Ref: "npm:@acme/billing-mcp", Private: true}, OK: true}, {}}
	if !slices.Equal(answers, want) {
		t.Fatalf("answers = %+v, want %+v", answers, want)
	}

	for _, mode := range []string{"too-few", "too-much", "fail"} {
		if got, err := resolvePackagesWith(helperResolver(context.Background(), mode), launchpkg.RegistryEnv{Home: home}, testQueries); err == nil {
			t.Errorf("%s: answers = %+v, want an error", mode, got)
		}
	}
}

// The subcommand speaks JSON both ways, and only what the request names is read.
func TestResolvePackagesCommand(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(work+"/.npmrc", []byte("registry=https://npm.acme.internal/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(packageRequest{Env: launchpkg.RegistryEnv{Home: t.TempDir()}, Queries: []launchpkg.Query{
		{Launch: testQueries[0].Launch},
		{Launch: testQueries[0].Launch, ProjectDirs: []string{work}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := endpointInventoryResolvePackagesCmd
	cmd.SetIn(bytes.NewReader(request))
	cmd.SetOut(&out)
	t.Cleanup(func() { cmd.SetIn(nil); cmd.SetOut(nil) })
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var answers []launchpkg.Answer
	if err := json.Unmarshal(out.Bytes(), &answers); err != nil {
		t.Fatal(err)
	}
	want := []launchpkg.Answer{
		{Package: launchpkg.Package{Ref: "npm:@acme/billing-mcp"}, OK: true},
		{Package: launchpkg.Package{Ref: "npm:@acme/billing-mcp", Private: true}, OK: true},
	}
	if !slices.Equal(answers, want) {
		t.Fatalf("answers = %+v, want %+v", answers, want)
	}
}

// The child is the console user's own process, so they can hold its stdout open after the timeout
// kills it. The job must still give up, or it would wait forever holding the inventory lock.
func TestResolvePackagesGivesUpOnAHeldPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got, err := resolvePackagesWith(helperResolver(ctx, "hold-stdout"), launchpkg.RegistryEnv{}, testQueries); err == nil {
		t.Fatalf("answers = %+v, want an error", got)
	}
	if elapsed := time.Since(start); elapsed > packageResolveWaitDelay+10*time.Second {
		t.Fatalf("resolvePackagesWith took %v; a process holding the pipe kept it waiting", elapsed)
	}
}
