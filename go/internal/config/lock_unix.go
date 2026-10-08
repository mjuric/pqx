//go:build unix

package config

import (
	"os"
	"syscall"
)

// haveLock: flock exists here.
const haveLock = true

// lock holds an exclusive flock on the sidecar file f (the lock Python's
// config._locked takes, with fcntl.flock: the two interoperate).
func lock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// newFileMode is the mode of a new file: 0666 less the umask.
func newFileMode() os.FileMode {
	u := syscall.Umask(0)
	syscall.Umask(u)
	return os.FileMode(0o666 &^ u)
}
