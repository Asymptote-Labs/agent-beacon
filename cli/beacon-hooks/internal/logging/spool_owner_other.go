//go:build !unix

package logging

// rootInAnotherAccountsWorkspace is never true off Unix, where no hook runs as uid 0.
func rootInAnotherAccountsWorkspace(string) bool { return false }
