package onboarding

import "os"

// keepOwner is a no-op on Windows: a file created in the person's profile inherits that
// directory's ACL, which grants them full control.
func keepOwner(*os.File, string) error { return nil }
