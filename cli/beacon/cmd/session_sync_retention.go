package cmd

import (
	"fmt"
	"io"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/spf13/pflag"
)

// The --rotate-bytes flag and the retention report every `beacon endpoint <runtime> sync` shares.
// Claude Code and Devin CLI report pending sessions as well and keep their own report.

func addSyncRotateBytesFlag(f *pflag.FlagSet, p *int64) {
	f.Int64Var(p, "rotate-bytes", 0,
		"Rotate the runtime log at this many bytes while this sync writes it (default 10485760, the size every Beacon writer uses; must not be smaller)")
}

// checkSyncRotateBytes refuses a size smaller than the shared one, which would only make this sync
// rotate more often than every other writer, shrinking what the log keeps for everyone.
func checkSyncRotateBytes(n int64) error {
	if n != 0 && n < writer.DefaultRotateBytes {
		return fmt.Errorf("--rotate-bytes must be at least %d, the size every Beacon writer rotates the runtime log at", writer.DefaultRotateBytes)
	}
	return nil
}

func reportSyncRetention(out io.Writer, r sessionsync.Retention) {
	if r.RetentionLimited {
		fmt.Fprint(out, "  stopped before log rotation would discard this sweep's own events; the rest is left for the next sweep.\n"+
			"  Ship or copy the runtime log before the next sweep to keep every event, or sync in one sweep with a larger --rotate-bytes.\n")
	}
	if r.EventsRotatedOut > 0 {
		fmt.Fprintf(out, "  %d event(s) this sweep wrote were rotated out of the runtime log by another writer before it finished\n",
			r.EventsRotatedOut)
	}
}
