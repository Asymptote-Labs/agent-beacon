//go:build unix

package localingest

import "syscall"

// openNoFollow and openNonBlock make openSpoolFile refuse a link at the name and not wait on a FIFO.
const (
	openNoFollow = syscall.O_NOFOLLOW
	openNonBlock = syscall.O_NONBLOCK
)
