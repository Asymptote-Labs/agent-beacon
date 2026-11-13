//go:build unix

package logging

import (
	"os"
	"syscall"
)

// rootInAnotherAccountsWorkspace reports whether this hook runs as root in a workspace another
// account owns -- typically a rootful container sandbox running the agent as uid 0 over the user's
// bind-mounted checkout. Root must neither stage nor drain there. Staging would leave root-owned
// 0700/0600 spool files the workspace's owner can neither drain (spool drain refuses root) nor
// delete. Draining would rewrite the owner's pending.jsonl as root's, so the owner's own hooks
// could no longer append to it. Root that owns the workspace -- uid 0 in a rootless container, the
// user mapped -- stages and drains as any account does.
func rootInAnotherAccountsWorkspace(workspace string) bool {
	if os.Geteuid() != 0 {
		return false
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return true
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return !ok || st.Uid != 0
}
