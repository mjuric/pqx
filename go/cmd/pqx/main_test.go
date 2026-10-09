package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// TestMain lets the pty tests run this test binary as pqx: with
// PQX_TEST_MAIN set it is pqx (see pty_test.go).
func TestMain(m *testing.M) {
	switch os.Getenv("PQX_TEST_MAIN") {
	case "":
		os.Exit(m.Run())
	case "panic":
		os.Exit(panicMain(os.Args[1]))
	default:
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
}

type golden struct {
	Cases []struct {
		Args   []string `json:"args"`
		Code   int      `json:"code"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
	} `json:"cases"`
}

// The command lines of internal/opts/testdata/cli.json, run as Python pqx
// ran them (with $COLUMNS 80): the same exit code and output.
func TestCommandLinesMatchPython(t *testing.T) {
	b, err := os.ReadFile("../../internal/opts/testdata/cli.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLUMNS", "80")
	defer func(v string) { version = v }(version)
	version = "{VERSION}"
	for _, c := range g.Cases {
		var out, errb bytes.Buffer
		code := run(c.Args, &out, &errb)
		if code != c.Code || out.String() != c.Stdout || errb.String() != c.Stderr {
			t.Errorf("%q: exit %d, stdout %q, stderr %q\nwant exit %d, stdout %q, stderr %q",
				c.Args, code, cut(out.String()), errb.String(), c.Code, cut(c.Stdout), c.Stderr)
		}
	}
}

func cut(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// controls are the C0 (but tab and newline), DEL and C1 characters in s,
// as test_security.py's controls(), and invalid UTF-8.
func controls(s string) []rune {
	var c []rune
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n') || (r >= 0x7f && r < 0xa0) || r == unicode.ReplacementChar {
			c = append(c, r)
		}
	}
	return c
}

// Port of test_security.py::test_cli_prints_no_control_characters, plus the
// command-line errors that quote what was typed.
func TestCLIPrintsNoControlCharacters(t *testing.T) {
	dir := t.TempDir()
	var errb bytes.Buffer
	if code := run([]string{filepath.Join(dir, "nope\x1b]0;T\x07.parquet")}, &bytes.Buffer{}, &errb); code != 2 {
		t.Errorf("missing file: exit %d", code)
	}
	bad := filepath.Join(dir, "bad\x1b]0;T\x07\u009b.parquet")
	if err := os.WriteFile(bad, []byte("not a parquet file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{bad}, &bytes.Buffer{}, &errb); code != 1 {
		t.Errorf("bad file: exit %d", code)
	}
	for _, args := range [][]string{
		{"--bogus\x1b]0;T\x07", bad},
		{"--accent", "\x1b[31m", bad},
		{"--format", "a\x1b]0;T\x07", bad},
		{"--th\x1b=1", bad},
		{"--border", "\x1b[31m", bad},
		{"--theme", "\x1b]0;T\x07", bad},
		{bad, "--threads", "-1"},
		{"\xff\x9b.parquet"},
	} {
		if code := run(args, &bytes.Buffer{}, &errb); code == 0 {
			t.Errorf("%q: exit 0", args)
		}
	}
	err := errb.String()
	if !strings.Contains(err, "␛]0;T") {
		t.Errorf("the file name isn't shown with ␛: %q", err)
	}
	if c := controls(err); len(c) > 0 {
		t.Errorf("control characters %q in %q", c, err)
	}
}

// The messages for files that can't be Parquet are PyArrow's, as Python
// pqx shows them.
func TestNotParquet(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]string{
		"":                     "Parquet file size is 0 bytes",
		"ab":                   "Parquet file size is 2 bytes, smaller than the minimum file footer (8 bytes)",
		"not a parquet file\n": "Parquet magic bytes not found in footer. Either the file is corrupted or this is not a parquet file.",
	} {
		p := filepath.Join(dir, "f.parquet")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		var errb bytes.Buffer
		if code := run([]string{p}, &bytes.Buffer{}, &errb); code != 1 || errb.String() != "pqx: cannot open "+p+": "+want+"\n" {
			t.Errorf("%q: exit %d, %q", content, code, errb.String())
		}
	}
}

// formats.yaml is read at startup; one that can't be read is ignored with
// Python's notice.
func TestSavedFormats(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if f, cmd := savedFormats(); len(f) != 0 || cmd != nil {
		t.Errorf("no file: %v %v", f, cmd)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pqx"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pqx", "formats.yaml")
	if err := os.WriteFile(path, []byte("columns:\n  ra: .2f\n  dec: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, cmd := savedFormats()
	if cmd != nil || f["ra"] != fmtx.ParseOverride(".2f") || f["dec"] != fmtx.ParseOverride("3") {
		t.Errorf("formats: %v %v", f, cmd)
	}
	st := newState(fakeDS{}, kit.Options{Formats: f, SessionFormats: map[string]fmtx.Override{"ra": fmtx.ParseOverride("1")}})
	if st.Formats["ra"] != fmtx.ParseOverride("1") || st.Formats["dec"] != fmtx.ParseOverride("3") {
		t.Errorf("state: --format must win over saved ones: %v", st.Formats)
	}
	if err := os.WriteFile(path, []byte("columns:\n  ra: [unclosed\n\x1b]0;T\x07"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, cmd = savedFormats()
	if f != nil || cmd == nil {
		t.Fatalf("corrupt file: %v %v", f, cmd)
	}
	n, ok := cmd().(kit.NotifyMsg)
	if !ok || n.Severity != kit.Warning || n.Title != "! Config" || n.Timeout != 8*time.Second ||
		!strings.HasPrefix(n.Text, "Ignoring saved column formats: ") || len(controls(n.Text)) > 0 {
		t.Errorf("notice: %+v", n)
	}
}

type fakeDS struct{ data.Unimplemented }
