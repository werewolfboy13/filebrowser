//go:build unix

package settings

import "syscall"

func syscallUmask(mask int) int { return syscall.Umask(mask) }
