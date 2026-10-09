package data

import (
	"errors"
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
	if li, err := os.Lstat(filepath.Join(extra, linkName)); err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Error("the link in a directory holding something else was removed")
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

// A link directory in use is never removed, however old: a second dataset
// opened a day later doesn't break the first.
func TestLinkOfAnOpenDatasetSurvives(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a*.parquet"), filepath.Join(dir, "b*.parquet")
	writeInts(t, a, 1, 2)
	writeInts(t, b, 3)
	ds, err := Open(a, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	link := ds.(*dataset).linkDir
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(link, old, old)
	ds2, err := Open(b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ds2.Close()
	if _, err := os.Lstat(filepath.Join(link, linkName)); err != nil {
		t.Fatal("the open dataset's link was removed")
	}
	if n, err := ds.Count(bg, View{Where: "a > 0"}); err != nil || n != 2 {
		t.Fatalf("Count %d %v", n, err)
	}
	if w, err := ds.Fetch(bg, View{SQL: "select a from t"}, 0, 5, []string{"a"}); err != nil || w.Len != 2 {
		t.Fatalf("SQL: %+v %v", w, err)
	}
	// once it is closed (or its process gone), it may go
	ds.Close()
	os.MkdirAll(link, 0o700)
	os.Symlink(a, filepath.Join(link, linkName))
	os.Chtimes(link, old, old)
	removeStaleLinks(filepath.Dir(link), staleAge)
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("a stale link directory nobody holds was left")
	}
}

// The links' base directory is used only if it is a private directory of
// this user's: not a link, not readable by others, not someone else's.
func TestLinkBaseChecks(t *testing.T) {
	tmp := t.TempDir()
	if _, ok := checkLinkBase(filepath.Join(tmp, "new")); !ok {
		t.Fatal("a new base refused")
	}
	if fi, _ := os.Stat(filepath.Join(tmp, "new")); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode())
	}
	real := filepath.Join(tmp, "real")
	os.Mkdir(real, 0o700)
	os.Symlink(real, filepath.Join(tmp, "link"))
	if _, ok := checkLinkBase(filepath.Join(tmp, "link")); ok {
		t.Error("a symbolic link used as the base")
	}
	open := filepath.Join(tmp, "open")
	os.Mkdir(open, 0o700)
	os.Chmod(open, 0o755)
	if _, ok := checkLinkBase(open); ok {
		t.Error("a base readable by others used")
	}
	if _, ok := checkLinkBase(real); !ok {
		t.Error("a good base refused")
	}
	saved := ownedByMe
	ownedByMe = func(os.FileInfo) bool { return false }
	defer func() { ownedByMe = saved }()
	if _, ok := checkLinkBase(real); ok {
		t.Error("someone else's base used")
	}
}

// The bind is tried again when DuckDB says it can't find a file that is
// there; a file that is gone fails at once.
func TestBindRetry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.parquet")
	writeInts(t, p, 1, 2, 3)
	defer func() { bindHook = nil }()
	tries := 0
	bindHook = func(*dataset) error {
		tries++
		if tries <= 2 {
			return errors.New(`IO Error: No files found that match the pattern "x"`)
		}
		return nil
	}
	ds, err := Open(p, Options{})
	if err != nil || ds.SetupErr() != nil || tries != 3 {
		t.Fatalf("%v %v after %d tries", err, ds.SetupErr(), tries)
	}
	if n, err := ds.Count(bg, View{Where: "a > 1"}); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	ds.Close()
	tries = 0
	bindHook = func(*dataset) error {
		tries++
		return errors.New(`IO Error: No files found that match the pattern "x"`)
	}
	ds, _ = Open(p, Options{})
	if ds.SetupErr() == nil || tries != 5 {
		t.Fatalf("always failing: %v after %d tries", ds.SetupErr(), tries)
	}
	ds.Close()
	tries = 0
	bindHook = func(d *dataset) error {
		tries++
		os.Remove(d.duckPath)
		return errors.New(`IO Error: No files found that match the pattern "x"`)
	}
	t0 := time.Now()
	ds, _ = Open(p, Options{})
	if ds.SetupErr() == nil || tries != 1 || time.Since(t0) > time.Second {
		t.Fatalf("missing file: %v after %d tries, %v", ds.SetupErr(), tries, time.Since(t0))
	}
	ds.Close()
}
