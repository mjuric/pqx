package data

import (
	"os"
	"path/filepath"
	"strings"
)

// duckPathFor is the path DuckDB reads the file at path by. DuckDB's
// read_parquet takes a path for a glob pattern; pathLiteral escapes *, ? and
// [ in it, but DuckDB splits a pattern at backslashes too, so a name with a
// backslash next to glob characters (a\*.parquet) matches nothing however
// it's escaped. Such a file is read through a symbolic link with a plain
// name, in a new temporary directory (linkDir, removed by Close).
func duckPathFor(path string) (duckPath, linkDir string, err error) {
	if os.PathSeparator != '/' || !strings.ContainsRune(path, '\\') || !strings.ContainsAny(path, "*?[") {
		return path, "", nil
	}
	dir, err := os.MkdirTemp("", "pqx-")
	if err != nil {
		return "", "", err
	}
	link := filepath.Join(dir, "data.parquet")
	if err := os.Symlink(path, link); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return link, dir, nil
}

// removeLink removes the link duckPathFor made, if it made one.
func (d *dataset) removeLink() {
	if d.linkDir != "" {
		os.RemoveAll(d.linkDir)
	}
}
