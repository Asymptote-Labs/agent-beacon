//go:build !linux

package asymptoteobserve

// OnSandboxRoot is never true off Linux: no sandbox there assembles a throwaway root filesystem
// the account owns (bubblewrap is Linux-only).
func OnSandboxRoot(string) bool { return false }
