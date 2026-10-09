//go:build !unix

package data

import "os"

// ownedByMe: links are used only on Unix (duckPathFor).
func ownedByMe(os.FileInfo) bool { return false }
