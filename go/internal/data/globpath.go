package data

import (
	"os"
	"path/filepath"
	"strings"
)

// duckPathFor is the path DuckDB reads the file at path by. DuckDB's
// read_parquet takes a path for a glob pattern. pathLiteral escapes *, ? and
// [ in it, but that isn't enough: DuckDB splits a pattern at backslashes
// too, so a name with a backslash next to glob characters (a\*.parquet)
// matches nothing however it's escaped, and an escaped pattern in a
// directory name (d*[x]/f.parquet) sometimes matches nothing, depending on
// the other names in the directory. So a path with glob characters is read
// through a symbolic link with a plain name, in a new temporary directory
// (linkDir, removed by Close); if that can't be made, the escaped path.
func duckPathFor(path string) (duckPath, linkDir string, err error) {
	if os.PathSeparator != '/' || !strings.ContainsAny(path, "*?[") {
		return path, "", nil
	}
	dir, err := os.MkdirTemp("", "pqx-")
	if err != nil {
		return path, "", nil
	}
	link := filepath.Join(dir, "data.parquet")
	if err := os.Symlink(path, link); err != nil {
		os.RemoveAll(dir)
		return path, "", nil
	}
	return link, dir, nil
}

// removeLink removes the link duckPathFor made, if it made one.
func (d *dataset) removeLink() {
	if d.linkDir != "" {
		os.RemoveAll(d.linkDir)
	}
}
