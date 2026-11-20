//go:build !windows

package mcpmanaged

import "syscall"

func syscallUmask(mask int) int { return syscall.Umask(mask) }
