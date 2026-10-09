package data

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// duckPathFor is the path DuckDB reads the file at path by. DuckDB's
// read_parquet takes a path for a glob pattern. pathLiteral escapes *, ? and
// [ in it, but that isn't enough: DuckDB splits a pattern at backslashes
// too, so a name with a backslash next to glob characters (a\*.parquet)
// matches nothing however it's escaped. So a path with glob characters is
// read through a symbolic link with a plain name, in a new directory of its
// own (linkDir, removed by Close) under linkBase; if that can't be made,
// the escaped path.
func duckPathFor(path string) (duckPath, linkDir string, err error) {
	if os.PathSeparator != '/' || !strings.ContainsAny(path, "*?[") {
		return path, "", nil
	}
	base, ok := linkBase()
	if !ok {
		return path, "", nil
	}
	removeStaleLinks(base, 24*time.Hour)
	dir, err := os.MkdirTemp(base, "")
	if err != nil {
		return path, "", nil
	}
	link := filepath.Join(dir, linkName)
	if err := os.Symlink(path, link); err != nil {
		os.RemoveAll(dir)
		return path, "", nil
	}
	return link, dir, nil
}

const linkName = "data.parquet"

// linkBase is the directory the links go in, one per user: made private
// (0700) if it isn't there, and used only if it is a real directory (not a
// link) owned by this user and private.
func linkBase() (string, bool) {
	base := filepath.Join(os.TempDir(), fmt.Sprintf("pqx-links-%d", os.Getuid()))
	if err := os.Mkdir(base, 0o700); err != nil && !os.IsExist(err) {
		return "", false
	}
	fi, err := os.Lstat(base)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ownedByMe(fi) {
		return "", false
	}
	return base, true
}

// removeStaleLinks removes what pqx left behind in base when it didn't get
// to Close (killed, crashed): directories older than age that hold nothing
// but one symbolic link named data.parquet. Nothing is followed, and
// anything else is left alone.
func removeStaleLinks(base string, age time.Duration) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || !ownedByMe(fi) || time.Since(fi.ModTime()) < age {
			continue
		}
		inner, err := os.ReadDir(dir)
		if err != nil || len(inner) != 1 || inner[0].Name() != linkName {
			continue
		}
		link := filepath.Join(dir, linkName)
		li, err := os.Lstat(link)
		if err != nil || li.Mode()&os.ModeSymlink == 0 || !ownedByMe(li) {
			continue
		}
		if os.Remove(link) == nil {
			os.Remove(dir) // (only if empty)
		}
	}
}

// removeLink removes the link duckPathFor made, if it made one.
func (d *dataset) removeLink() {
	if d.linkDir != "" {
		os.Remove(filepath.Join(d.linkDir, linkName))
		os.Remove(d.linkDir)
	}
}
