//go:build !unix

package asymptoteobserve

import "os"

// markerOwnedByRoot never trusts a marker off Unix, where restricted log mode does not exist.
func markerOwnedByRoot(os.FileInfo) bool { return false }

// settleRestrictedLogGroup has nothing to do off Unix.
func settleRestrictedLogGroup(os.FileInfo, int, func(uid, gid int) error) error { return nil }
