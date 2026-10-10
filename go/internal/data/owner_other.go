//go:build !unix

package data

import (
	"errors"
	"os"
)

// Links are used only on Unix (duckPathFor).

var ownedByMe = func(os.FileInfo) bool { return false }

func holdDir(string) (*os.File, error) { return nil, errors.New("no links here") }

func unusedDir(string) (*os.File, bool) { return nil, false }

// writable reports whether the existing file at path may be written: on
// Windows, whether its read-only attribute is clear (Go maps it to the 0200 bit).
var writable = func(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm()&0o200 != 0
}
