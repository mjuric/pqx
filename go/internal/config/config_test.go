package config

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

func spec(s string) fmtx.Override { return fmtx.Override{Spec: s, Set: true} }
func digits(n int) fmtx.Override  { return fmtx.Override{Digits: n, Set: true} }

func configHome(t *testing.T) string {
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	return d
}

func mustLoad(t *testing.T, p string) map[string]fmtx.Override {
	t.Helper()
	m, err := LoadFormats(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// test_config.py::test_save_load_merge
func TestSaveLoadMerge(t *testing.T) {
	home := configHome(t)
	p := Path()
	if want := filepath.Join(home, "pqx", "formats.yaml"); p != want {
		t.Fatalf("Path() = %q, want %q", p, want)
	}
	if m := mustLoad(t, ""); len(m) != 0 {
		t.Fatalf("no file: %v", m)
	}
	for _, c := range []struct {
		n string
		o fmtx.Override
	}{{"ra", spec(".4f")}, {"psfFlux", digits(3)}, {"odd", spec(".5")}} {
		if _, err := SaveFormat(c.n, c.o, ""); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(p)
	text := string(b)
	if !strings.HasPrefix(text, "# pqx column display formats") {
		t.Fatalf("no header: %q", text)
	}
	want := map[string]fmtx.Override{"ra": spec(".4f"), "psfFlux": digits(3), "odd": spec(".5")}
	if m := mustLoad(t, ""); !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}
	// another session's change is kept: save re-reads the file before writing
	os.WriteFile(p, []byte(text+"  dec: .2f\n"), 0o644)
	if _, err := SaveFormat("ra", fmtx.Override{}, ""); err != nil {
		t.Fatal(err)
	}
	want = map[string]fmtx.Override{"psfFlux": digits(3), "odd": spec(".5"), "dec": spec(".2f")}
	if m := mustLoad(t, ""); !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}
	if tmp, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "*.tmp")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
}

// test_config.py::test_hand_edited_and_corrupt
func TestHandEditedAndCorrupt(t *testing.T) {
	configHome(t)
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("columns:\n  ra: .3f\n  bad: [1, 2]\n  neg: -1\n  empty: ''\n"), 0o644)
	if m := mustLoad(t, ""); !reflect.DeepEqual(m, map[string]fmtx.Override{"ra": spec(".3f")}) {
		t.Fatalf("got %v", m)
	}
	os.WriteFile(p, []byte("columns: [ra\n"), 0o644)
	if _, err := LoadFormats(""); !errors.Is(err, ErrConfig) {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := SaveFormat("ra", digits(2), ""); !errors.Is(err, ErrConfig) { // never overwrite a file we can't parse
		t.Fatalf("save over corrupt: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "columns: [ra\n" {
		t.Fatalf("overwritten: %q", b)
	}
	os.WriteFile(p, []byte("columns: 3\n"), 0o644)
	if _, err := LoadFormats(""); !errors.Is(err, ErrConfig) {
		t.Fatalf("columns not a mapping: %v", err)
	}
	for _, s := range []string{"", "[]\n", "columns:\n", "other: 1\n", "3\n"} {
		os.WriteFile(p, []byte(s), 0o644)
		if m, err := LoadFormats(""); err != nil || len(m) != 0 {
			t.Fatalf("%q: %v %v", s, m, err)
		}
	}
}

// test_config.py::test_symlink_mode_and_bad_values
func TestSymlinkModeAndBadValues(t *testing.T) {
	configHome(t)
	real := filepath.Join(t.TempDir(), "dotfiles", "formats.yaml")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("columns:\n  ra: .3f\n  huge: 100000000\n  slow: .100000000f\n"), 0o644)
	os.Chmod(real, 0o644)
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(real, p); err != nil {
		t.Fatal(err)
	}
	if m := mustLoad(t, ""); !reflect.DeepEqual(m, map[string]fmtx.Override{"ra": spec(".3f")}) {
		t.Fatalf("out-of-range entries kept: %v", m)
	}
	if _, err := SaveFormat("dec", digits(2), ""); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Lstat(p)
	rst, _ := os.Stat(real)
	if st.Mode()&os.ModeSymlink == 0 || rst.Mode().Perm() != 0o644 {
		t.Fatalf("link replaced or mode changed: %v %v", st.Mode(), rst.Mode())
	}
	if m := mustLoad(t, ""); !reflect.DeepEqual(m, map[string]fmtx.Override{"ra": spec(".3f"), "dec": digits(2)}) {
		t.Fatalf("got %v", m)
	}
}

func TestDanglingSymlinkIsFollowed(t *testing.T) {
	configHome(t)
	real := filepath.Join(t.TempDir(), "dotfiles", "formats.yaml")
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.Symlink(real, p)
	got, err := SaveFormat("ra", spec(".2f"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Fatalf("wrote %q, want %q", got, real)
	}
}

// test_config.py::test_binary_file_is_a_config_error
func TestBinaryFileIsAConfigError(t *testing.T) {
	configHome(t)
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("\xff\xfe\x00columns"), 0o644)
	if _, err := LoadFormats(""); !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestUnreadableIsAConfigError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	configHome(t)
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("columns: {}\n"), 0o000)
	if _, err := LoadFormats(""); !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestPathIgnoresRelativeXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	home, _ := os.UserHomeDir()
	if got, want := Path(), filepath.Join(home, ".config", "pqx", "formats.yaml"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNewFileModeFollowsUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	configHome(t)
	p, err := SaveFormat("ra", spec(".2f"), "")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != newFileMode() {
		t.Fatalf("mode %v, want %v", st.Mode().Perm(), newFileMode())
	}
}

func TestUnchangedSaveDoesNotWrite(t *testing.T) {
	configHome(t)
	p, _ := SaveFormat("ra", spec(".2f"), "")
	st1, _ := os.Stat(p)
	os.Chtimes(p, st1.ModTime().Add(-3600e9), st1.ModTime().Add(-3600e9))
	st1, _ = os.Stat(p)
	SaveFormat("ra", spec(".2f"), "")
	SaveFormat("other", fmtx.Override{}, "")
	st2, _ := os.Stat(p)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("rewritten")
	}
}

// Sessions saving at once each keep their column (the lock and re-read).
func TestConcurrentSaves(t *testing.T) {
	configHome(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := SaveFormat("c"+string(rune('a'+i)), digits(i%17), ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if m := mustLoad(t, ""); len(m) != 16 {
		t.Fatalf("lost saves: %v", m)
	}
}

func TestYAMLResolution(t *testing.T) {
	configHome(t)
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("columns:\n  a: 017\n  b: 0x3\n  c: yes\n  d: .5\n  e: '.5'\n  f: 1_0\n  g: ~\n  h: \"%Y\"\n  1: 2\n  i: 0:5\n  j: 2026-01-02\n  k: 1:05\n"), 0o644)
	want := map[string]fmtx.Override{"a": digits(15), "b": digits(3), "e": spec(".5"), "f": digits(10), "h": spec("%Y"),
		"1": digits(2)}
	if m := mustLoad(t, ""); !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}
}

// python is the reference Python with this checkout's pqx, or a skip.
func python(t *testing.T) func(code string, args ...string) string {
	t.Helper()
	py := "/root/parquet-explorer/.venv/bin/python"
	if _, err := os.Stat(py); err != nil {
		t.Skip("no reference Python at " + py)
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(file), "..", "..", "..")
	if _, err := os.Stat(filepath.Join(repo, "pqx", "config.py")); err != nil {
		t.Skip("no Python pqx in the checkout")
	}
	return func(code string, args ...string) string {
		t.Helper()
		cmd := exec.Command(py, append([]string{"-c", code}, args...)...)
		cmd.Env = append(os.Environ(), "PYTHONPATH="+repo)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("python: %v\n%s", err, out)
		}
		return string(out)
	}
}

// Python writes, Go reads and writes, Python reads: the file is shared.
func TestRoundTripWithPython(t *testing.T) {
	py := python(t)
	p := filepath.Join(t.TempDir(), "pqx", "formats.yaml")
	py(`import sys
from pathlib import Path
from pqx import config
p = Path(sys.argv[1])
for k, v in [("ra", ".4f"), ("psfFlux", 3), ("odd", ".5"), ("t", "%Y-%m-%d %H:%M"), ("weird name", ",d"),
             ("yes", ">12"), ("é ✓", "+.3g"), ("q'uote", "#x"), ("k: v", "_d"), ("-x", "^10"), ("tab\there", 0),
             ("esc\x1b]0;x", ".2%"), ("x", "x")]:
    config.save_format(k, v, p)
`, p)
	want := map[string]fmtx.Override{"ra": spec(".4f"), "psfFlux": digits(3), "odd": spec(".5"),
		"t": spec("%Y-%m-%d %H:%M"), "weird name": spec(",d"), "yes": spec(">12"), "é ✓": spec("+.3g"),
		"q'uote": spec("#x"), "k: v": spec("_d"), "-x": spec("^10"), "tab\there": digits(0),
		"esc\x1b]0;x": spec(".2%"), "x": spec("x")}
	if m := mustLoad(t, p); !reflect.DeepEqual(m, want) {
		t.Fatalf("Go read %v\nwant %v", m, want)
	}
	before, _ := os.ReadFile(p)
	// Go rewrites the file with the same contents plus one change: byte for byte what PyYAML writes
	if _, err := SaveFormat("zz", spec(".1e"), p); err != nil {
		t.Fatal(err)
	}
	py(`import sys
from pathlib import Path
from pqx import config
config.save_format("zz", None, Path(sys.argv[1]))
`, p)
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatalf("Go's file differs from Python's:\n%s\n---\n%s", before, after)
	}
	SaveFormat("zz", spec(".1e"), p)
	SaveFormat("ra", fmtx.Override{}, p)
	// names PyYAML writes as "? key" (Go doesn't fold long lines as PyYAML does)
	long := strings.Repeat("long ", 30)
	SaveFormat("", digits(4), p)
	SaveFormat(long, spec("<12"), p)
	SaveFormat("two\nlines", spec("a\nb%Y"), p)
	got := py(`import sys, json
from pathlib import Path
from pqx import config
print(json.dumps(config.load_formats(Path(sys.argv[1]))))
`, p)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	delete(want, "ra")
	want["zz"] = spec(".1e")
	want[""] = digits(4)
	want[long] = spec("<12")
	want["two\nlines"] = spec("a\nb%Y")
	if len(m) != len(want) {
		t.Fatalf("Python read %v", m)
	}
	for k, v := range want {
		var g fmtx.Override
		switch x := m[k].(type) {
		case float64:
			g = digits(int(x))
		case string:
			g = spec(x)
		}
		if g != v {
			t.Errorf("Python read %q as %v, want %v", k, m[k], v)
		}
	}
}

// Go's YAML text is PyYAML's for awkward names and specs.
func TestYAMLMatchesPyYAML(t *testing.T) {
	py := python(t)
	keys := []string{"ra", " lead", "trail ", "yes", "No", "null", "~", "1", "0x1f", "1.5", ".5", "1e3", "2026-01-02",
		"a: b", "a:b", "a #b", "a#b", "#x", "-", "- x", "-x", "?x", "? x", ":x", "[x]", "x,y", "{", "&a", "*a", "!t",
		"|", ">", "'q", "\"q", "%x", "@x", "`x", "---", "...x", "é", "漢字", "😀", "\x1b[2J", "\u009b", "\u202e", "\ufeff",
		"a\u00a0b", "tab\tx", "=", "<<", "on", "x'y", "back\\slash", "\x00"}
	b, _ := json.Marshal(keys)
	out := py(`import sys, json, yaml
keys = json.loads(sys.argv[1])
print(json.dumps([yaml.safe_dump({"columns": {k: k}}, sort_keys=False, allow_unicode=True, default_flow_style=False) for k in keys]))
`, string(b))
	var want []string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		got := dump(map[string]fmtx.Override{k: spec(k)})
		if got != want[i] {
			t.Errorf("%q: got %q, PyYAML %q", k, got, want[i])
		}
	}
}

// A Python session holding the lock makes Go's save wait for it (flock on
// the same sidecar file).
func TestLockInteroperatesWithPython(t *testing.T) {
	if !haveLock {
		t.Skip("no flock")
	}
	python(t) // skips without the reference Python
	p := filepath.Join(t.TempDir(), "formats.yaml")
	cmd := exec.Command("/root/parquet-explorer/.venv/bin/python", "-c", `import sys, time
from pathlib import Path
from pqx import config
p = Path(sys.argv[1])
with config._locked(p):
    print("locked", flush=True)
    time.sleep(0.8)
    p.write_text("columns:\n  py: .1f\n")
`, p)
	_, file, _, _ := runtime.Caller(0)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(filepath.Dir(file), "..", "..", ".."))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := out.Read(buf); err != nil || string(buf) != "locked\n" {
		t.Fatalf("python: %q %v", buf, err)
	}
	start := time.Now()
	if _, err := SaveFormat("go", digits(2), p); err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	cmd.Wait()
	if waited < 300*time.Millisecond {
		t.Fatalf("didn't wait for Python's lock (%v)", waited)
	}
	want := map[string]fmtx.Override{"py": spec(".1f"), "go": digits(2)}
	if m := mustLoad(t, p); !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}
}
