//go:build !windows

package writer

// POSIX carries this in the file mode instead: openRuntimeFile creates the runtime log 0666 so
// hooks running as the console user can append to a log the elevated collector created. There is
// no separate ACL step to perform, and no separate state to check.

func grantInteractiveUsersWrite(dir string) error { return nil }

// interactiveUsersCanWrite reports true because the equivalent guarantee is the file mode, which
// EnsureRuntimeFile already establishes. Reporting false here would make doctor demand a fix that
// does not exist on this platform.
func interactiveUsersCanWrite(dir string) (bool, error) { return true, nil }
