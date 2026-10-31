//go:build !windows

package asymptoteobserve

// runtimeLogRenamable is always nil off Windows, where a rename or delete does not care who has the
// file open.
func runtimeLogRenamable(string) error { return nil }
