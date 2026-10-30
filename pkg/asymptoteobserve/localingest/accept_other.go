//go:build !unix

package localingest

// temporaryAcceptError is never true off Unix, where nothing serves the socket.
func temporaryAcceptError(error) bool { return false }
