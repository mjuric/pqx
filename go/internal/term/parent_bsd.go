//go:build dragonfly || freebsd || netbsd || openbsd

package term

import (
	"errors"

	"golang.org/x/sys/unix"
)

// parentOf is pid's parent; only pqx's own is known here.
func parentOf(pid int) (int, error) {
	if pid == unix.Getpid() {
		return unix.Getppid(), nil
	}
	return 0, errors.New("parent unknown")
}
