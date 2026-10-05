// Command e2eserver runs the real dashboard handler for the lens browser tests in
// testdata/lens-e2e. It is the dashboard plus lens files from disk (Options.LensFiles), so the tests
// can serve hostile lenses through the same frame route, CSP and prelude a user's lens gets.
package main

import (
	"flag"
	"fmt"
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
	err := dashboard.ListenAndServe(dashboard.Options{Addr: *addr, LogPath: *logPath, UserMode: true, LensFiles: lenses})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
