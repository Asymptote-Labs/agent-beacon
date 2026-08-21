//go:build !windows

package cmd

// windowsActiveConsoleUser is unreachable here -- defaultActiveConsoleUser dispatches on
// runtime.GOOS -- and exists so that function compiles as one piece rather than being split per
// platform for the sake of one branch.
func windowsActiveConsoleUser() (consoleUserInfo, bool, error) {
	return consoleUserInfo{}, false, nil
}
