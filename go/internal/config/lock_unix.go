//go:build unix

package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
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

var (
	umaskOnce sync.Once
	umask     int
)

// newFileMode is the mode of a new file: 0666 less the umask. The umask
// is read once: from /proc/self/status where there is one, else by setting
// and restoring it (which another thread creating a file at that moment
// could see).
func newFileMode() os.FileMode {
	umaskOnce.Do(func() {
		if b, err := os.ReadFile("/proc/self/status"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "Umask:"); ok {
					if n, err := strconv.ParseInt(strings.TrimSpace(v), 8, 32); err == nil {
						umask = int(n)
						return
					}
				}
			}
		}
		umask = syscall.Umask(0)
		syscall.Umask(umask)
	})
	return os.FileMode(0o666 &^ umask)
}
