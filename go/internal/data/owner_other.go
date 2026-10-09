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
