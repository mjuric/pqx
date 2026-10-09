package data

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Links left behind by a pqx that didn't Close are removed after a day; nothing
// else is touched or followed.
func TestRemoveStaleLinks(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(t.TempDir(), "x.parquet")
	os.WriteFile(target, []byte("keep me"), 0o600)
	old := time.Now().Add(-48 * time.Hour)
	mk := func(name string, files map[string]string, age time.Time) string {
		dir := filepath.Join(base, name)
		os.Mkdir(dir, 0o700)
		for f, link := range files {
			if link != "" {
				os.Symlink(link, filepath.Join(dir, f))
			} else {
				os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600)
			}
		}
		os.Chtimes(dir, age, age)
		return dir
	}
	stale := mk("stale", map[string]string{linkName: target}, old)
	fresh := mk("fresh", map[string]string{linkName: target}, time.Now())
	extra := mk("extra", map[string]string{linkName: target, "other": ""}, old)
	file := mk("file", map[string]string{linkName: ""}, old)
	linked := t.TempDir()
	os.WriteFile(filepath.Join(linked, "f"), []byte("x"), 0o600)
	dirLink := mk("dirlink", map[string]string{linkName: linked}, old) // the link goes, not what it points to
	os.Symlink(t.TempDir(), filepath.Join(base, "alink"))              // a link to a directory: not followed
	removeStaleLinks(base, 24*time.Hour)
	if _, err := os.Lstat(stale); err == nil {
		t.Error("a stale link directory was left")
	}
	if _, err := os.Lstat(dirLink); err == nil {
		t.Error("a stale link to a directory was left")
	}
	if _, err := os.Stat(filepath.Join(linked, "f")); err != nil {
		t.Error("a link was followed")
	}
	for _, d := range []string{fresh, extra, file, filepath.Join(base, "alink")} {
		if _, err := os.Lstat(d); err != nil {
			t.Errorf("%s removed", d)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep me" {
		t.Error("the target changed")
	}
}

// Open and Close of a file with glob characters leave no link behind.
func TestLinkRemovedOnClose(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a*b.parquet")
	writeInts(t, p, 1)
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := ds.(*dataset).linkDir
	if dir == "" {
		t.Fatal("no link")
	}
	if fi, err := os.Lstat(filepath.Dir(dir)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("link base: %v %v", fi, err)
	}
	ds.Close()
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("link dir left behind")
	}
}
