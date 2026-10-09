//go:build unix

package data

import (
	"os"
	"syscall"
)

// ownedByMe reports whether the file fi describes belongs to this user.
// (A variable for tests.)
var ownedByMe = func(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// holdDir takes a shared lock on directory dir, held until the returned
// file is closed: a link directory in use.
func holdDir(dir string) (*os.File, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// unusedDir takes an exclusive lock on directory dir without waiting: ok
// if no one holds it. The lock goes with the returned file's Close.
func unusedDir(dir string) (*os.File, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return f, true
}
