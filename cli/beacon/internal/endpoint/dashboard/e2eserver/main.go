// Command e2eserver runs the real dashboard handler for the lens browser tests in
// testdata/lens-e2e. It is the dashboard plus lens files from disk (Options.LensFiles), so the tests
// can serve hostile lenses through the same frame route, CSP and prelude a user's lens gets.
//
// It serves behind the same launch secret as `beacon endpoint dashboard`. The tests pass theirs in
// $BEACON_E2E_DASHBOARD_TOKEN (the environment, not argv, which other accounts can read); without
// one it makes its own and prints the launch URL.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
)

type lensFlags []string

func (l *lensFlags) String() string     { return strings.Join(*l, ",") }
func (l *lensFlags) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var lenses lensFlags
	addr := flag.String("addr", "127.0.0.1:8798", "loopback listen address")
	logPath := flag.String("log", "", "runtime JSONL log to serve")
	flag.Var(&lenses, "lens", "lens file to serve (repeatable)")
	flag.Parse()
	token := os.Getenv("BEACON_E2E_DASHBOARD_TOKEN")
	printURL := false
	if token == "" {
		printURL = true
		var err error
		if token, err = dashboard.NewToken(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	err := dashboard.ListenAndServe(dashboard.Options{
		Addr: *addr, LogPath: *logPath, UserMode: true, LensFiles: lenses, Token: token,
		Ready: func(bound net.Addr) error {
			if printURL {
				fmt.Println(dashboard.LaunchURL(bound.String(), token, "/"))
			}
			return nil
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
