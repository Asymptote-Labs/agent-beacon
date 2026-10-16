//go:build !unix

package asymptoteobserve

import "os"

// processOwnsHome is always true off Unix: file modes there do not decide who can append (Windows
// uses the profile's ACL), so the root-in-another-home hazard does not arise.
func processOwnsHome(string) bool { return true }

// ownedByProcess is always true off Unix, where file modes do not decide who can read the log.
func ownedByProcess(os.FileInfo) bool { return true }

// elevatedOpenFlags adds nothing off Unix.
func elevatedOpenFlags() int { return 0 }

// linkedElsewhere is never true off Unix, where no root writer opens the log through a directory
// another user can plant links in.
func linkedElsewhere(os.FileInfo) bool { return false }
