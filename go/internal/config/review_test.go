package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// Tests from the review of WP3.

func TestModeIsKept(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o640, 0o604, 0o664} {
		p := filepath.Join(t.TempDir(), "formats.yaml")
		os.WriteFile(p, []byte("columns:\n  ra: .3f\n"), 0o644)
		os.Chmod(p, mode)
		if _, err := SaveFormat("dec", digits(2), p); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm() != mode {
			t.Errorf("mode %v, want %v", st.Mode().Perm(), mode)
		}
	}
}

func TestSaveSyncsBeforeReplacing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "formats.yaml")
	var synced []string
	old := syncFile
	syncFile = func(f *os.File) error {
		synced = append(synced, f.Name())
		if _, err := os.Stat(p); err == nil {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "dec:") {
				t.Error("replaced before the sync")
			}
		}
		return old(f)
	}
	defer func() { syncFile = old }()
	SaveFormat("ra", spec(".2f"), p)
	SaveFormat("dec", spec(".2f"), p)
	if len(synced) != 2 || !strings.HasPrefix(filepath.Base(synced[0]), ".formats.") {
		t.Fatalf("synced %v", synced)
	}
	// a failed sync leaves the file as it was, and no temporary file
	syncFile = func(*os.File) error { return errors.New("disk full") }
	if _, err := SaveFormat("x", spec(".1f"), p); err == nil {
		t.Fatal("no error")
	}
	if m := mustLoad(t, p); len(m) != 2 {
		t.Fatalf("got %v", m)
	}
	if tmp, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "*.tmp")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
}

func TestStaleTempFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	stale, fresh := filepath.Join(dir, ".formats.old.tmp"), filepath.Join(dir, ".formats.new.tmp")
	os.WriteFile(stale, nil, 0o600)
	os.WriteFile(fresh, nil, 0o600)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, old, old)
	if _, err := SaveFormat("ra", spec(".2f"), filepath.Join(dir, "formats.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale temp file kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("another save's temp file removed")
	}
}

// A name of 128 characters or more is not a simple key for PyYAML.
func TestLongKeys(t *testing.T) {
	k127, k128 := strings.Repeat("x", 127), strings.Repeat("y", 128)
	got := dump(map[string]fmtx.Override{k127: digits(1), k128: digits(2)})
	want := "columns:\n  " + k127 + ": 1\n  ? " + k128 + "\n  : 2\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	p := filepath.Join(t.TempDir(), "formats.yaml")
	os.WriteFile(p, []byte(header+got), 0o644)
	if m := mustLoad(t, p); !reflect.DeepEqual(m, map[string]fmtx.Override{k127: digits(1), k128: digits(2)}) {
		t.Fatalf("read %v", m)
	}
}

func TestYAMLReadsAsPyYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "formats.yaml")
	read := func(text string) (map[string]fmtx.Override, error) {
		os.WriteFile(p, []byte(text), 0o644)
		return LoadFormats(p)
	}
	for text, want := range map[string]map[string]fmtx.Override{
		// single-quoted over several lines: a blank line is a newline
		"columns:\n  ? 'a\n\n    b'\n  : .2f\n": {"a\nb": spec(".2f")},
		"columns:\n  a: '%Y\n\n    %m'\n":       {"a": spec("%Y\n%m")},
		// 08 and 09 aren't octal: text
		"columns:\n  a: 08\n  b: 07\n  c: 09\n": {"a": spec("08"), "b": digits(7), "c": spec("09")},
		// !!str makes digits a spec
		"columns:\n  a: !!str 3\n": {"a": spec("3")},
		// merge keys: the mapping's own keys win, then the first merged
		"a: &a {x: .1f, y: .1f}\nb: &b {x: .2f, z: .2f}\ncolumns:\n  <<: [*a, *b]\n  y: .3f\n": {"x": spec(".1f"), "y": spec(".3f"), "z": spec(".2f")},
		// keys as PyYAML builds them, then str()
		"columns:\n  .inf: 1\n  1_000.5: 2\n  2026-01-02 03:04:05.5Z: 3\n  !!int 0o7: 4\n  ~: 5\n  yes: 6\n": {"inf": digits(1),
			"1000.5": digits(2), "2026-01-02 03:04:05.500000+00:00": digits(3), "7": digits(4), "None": digits(5), "True": digits(6)},
		// 1, 1.0 and true are one key in Python: the first name, the last value
		"columns:\n  1: 3\n  true: 4\n  1.0: 5\n": {"1": digits(5)},
		// an = key is text
		"columns:\n  =: 4\n": {"=": digits(4)},
	} {
		got, err := read(text)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v %v, want %v", text, got, err, want)
		}
	}
	for _, text := range []string{
		"columns:\n  a: !!python/object:os.system .2f\n", "columns:\n  a: !custom 3\n", "x: !!python/name:os.system\n",
		"columns:\n  a: .2f\n---\nx: 1\n", "x: =\n", "x: [=]\n", "columns: !!set\n  ? a\n", "columns: !!omap\n  - a: 1\n",
		"x: !!omap\n  - a\n", "columns:\n  [a, b]: 3\n", "columns:\n  a: !!int x\n", "columns:\n  a: !!bool maybe\n",
		"columns:\n  2026-13-01: 3\n", "columns:\n  a: !!str [1]\n", "columns: !!map [1]\n", "columns:\n  <<: 3\n",
		"columns:\n  a: 3\x00\n", "columns:\n  'a\n\n   b': .2f\n", "columns:\n  a\x1b: 3\n", "columns:\n  a: 3\u0085  b: 4\n", "%YAML 2.0\n---\ncolumns: {}\n",
	} {
		if got, err := read(text); !errors.Is(err, ErrConfig) {
			t.Errorf("%q: got %v, want an error", text, got)
		}
		// and a save refuses to rewrite it
		if _, err := SaveFormat("ra", digits(2), p); !errors.Is(err, ErrConfig) {
			t.Errorf("%q: saved over it", text)
		}
	}
	for _, text := range []string{"%YAML 1.1\n---\ncolumns:\n  a: .2f\n", "%YAML 1.2\n---\ncolumns:\n  a: .2f\n"} {
		got, err := read(text)
		if err != nil || !reflect.DeepEqual(got, map[string]fmtx.Override{"a": spec(".2f")}) {
			t.Errorf("%q: got %v %v", text, got, err)
		}
	}
}
