//go:build !unix

package config

import "os"

// haveLock: no flock (Windows): no lock file either, as in Python pqx;
// saves are still atomic.
const haveLock = false

func lock(*os.File) error { return nil }
func unlock(*os.File)     {}

func newFileMode() os.FileMode { return 0o666 }
