//go:build unix

package data

import (
	"os"
	"syscall"
)

// ownedByMe reports whether the file fi describes belongs to this user.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
