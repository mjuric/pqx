package opts

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// Golden is testdata/cli.json, written by testdata/make_golden.py from
// Python pqx: --help by $COLUMNS, --version, and the outcome of command
// lines (the version replaced by {VERSION}).
type Golden struct {
	Help    map[string]string `json:"help"`
	Version string            `json:"version"`
	Cases   []struct {
		Args   []string `json:"args"`
		Code   int      `json:"code"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
	} `json:"cases"`
}

func loadGolden(t *testing.T) Golden {
	t.Helper()
	b, err := os.ReadFile("testdata/cli.json")
	if err != nil {
		t.Fatal(err)
	}
	var g Golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestHelpMatchesPython(t *testing.T) {
	g := loadGolden(t)
	if len(g.Help) == 0 {
		t.Fatal("no help texts in the golden file")
	}
	for cols, want := range g.Help {
		c, _ := strconv.Atoi(cols)
		if got := Help(c - 2); got != want {
			t.Errorf("COLUMNS=%d: got\n%s\nwant\n%s", c, got, want)
		}
	}
}

func TestVersionMatchesPython(t *testing.T) {
	g := loadGolden(t)
	if got := VersionText("{VERSION}") + "\n"; got != g.Version {
		t.Errorf("got %q, want %q", got, g.Version)
	}
}

// Port of test_branding.py::test_version_gnu_style.
func TestVersionGnuStyle(t *testing.T) {
	want := []string{
		"pqx 1.2.3",
		"Copyright (C) 2026 Mario Juric",
		"License BSD-3-Clause: <https://opensource.org/license/bsd-3-clause>",
		"This is free software: you are free to change and redistribute it.",
		"There is NO WARRANTY, to the extent permitted by law.",
		"",
		"Written by Mario Juric.",
	}
	got := VersionText("1.2.3")
	if got != strings.Join(want, "\n") {
		t.Errorf("got %q", got)
	}
	for _, r := range got {
		if r >= 0x80 {
			t.Errorf("not ASCII: %q", got)
		}
	}
}

// Port of test_branding.py::test_help_epilog.
func TestHelpEpilog(t *testing.T) {
	out := Help(80 - 2)
	if !strings.HasPrefix(out, "usage: pqx ") {
		t.Errorf("starts %q", out[:20])
	}
	if !strings.Contains(out, "--format COL=SPEC") || !strings.Contains(out, "--version") {
		t.Error("options missing")
	}
	want := "Examples:\n" +
		"  pqx trips.parquet\n" +
		"  pqx trips.parquet -w \"payment_type = 'card'\"\n" +
		"  pqx trips.parquet -w \"select vendor, count(*) from t group by 1\"\n" +
		"\n" +
		"Inside pqx, press ? for keys.\n" +
		"\n" +
		"Report bugs at: <https://github.com/mjuric/pqx/issues>\n" +
		"pqx home page: <https://github.com/mjuric/pqx>\n" +
		"Written by Mario Juric."
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), want) {
		t.Errorf("epilog:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if n(l) > 80 {
			t.Errorf("line longer than 80: %q", l)
		}
	}
}

// wrap breaks only at spaces, as textwrap does for text without hyphens.
func TestHelpHasNoHyphens(t *testing.T) {
	for _, s := range append([]string{description}, helps()...) {
		if strings.Contains(s, "-") {
			t.Errorf("help text with a hyphen (wrap doesn't break after hyphens as textwrap does): %q", s)
		}
	}
}

func helps() []string {
	var h []string
	for _, o := range options {
		h = append(h, o.help)
	}
	return h
}

func TestWrapLikeTextwrap(t *testing.T) {
	// outputs of textwrap.wrap(text, width)
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"aaa bbb ccc", 7, []string{"aaa bbb", "ccc"}},
		{"aaa bbb ccc", 6, []string{"aaa", "bbb", "ccc"}},
		{"see $XDG_CONFIG_HOME/pqx/formats.yaml now", 12, []string{"see $XDG_CON", "FIG_HOME/pqx", "/formats.yam", "l now"}},
		{"  lots   of\tspace  ", 20, []string{"lots of space"}},
		{"abcdefgh", 3, []string{"abc", "def", "gh"}},
		{"ab cdefghijk", 3, []string{"ab ", "cde", "fgh", "ijk"}}, // (sic)
		{"… — é", 3, []string{"… —", "é"}},
	}
	for _, c := range cases {
		if got := wrap(c.text, c.width); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
}

func TestPyRepr(t *testing.T) {
	cases := map[string]string{
		"x":          `'x'`,
		"a'b":        `"a'b"`,
		`a"b'`:       `'a"b\''`,
		"é\u200b":    `'é\u200b'`,
		"\x1b]0;T\a": `'\x1b]0;T\x07'`,
		"t\tn\n\\":   `'t\tn\n\\'`,
		"\x9b\xff":   `'\udc9b\udcff'`,
		"\u0085":     `'\x85'`,
		"\U0001F600": "'\U0001F600'",
	}
	for in, want := range cases {
		if got := pyRepr(in); got != want {
			t.Errorf("pyRepr(%q) = %s, want %s", in, got, want)
		}
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestOptions(t *testing.T) {
	o, err := Parse([]string{"f.parquet"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if o.Path != "f.parquet" || o.Where != "" || o.Accent != "blue" || o.Dim != "faint" ||
		o.Border != "ansi_bright_black" || o.Theme != "" || o.Sample != nil || o.Threads != 0 || len(o.Formats) != 0 {
		t.Errorf("defaults: %+v", o)
	}
	o, err = Parse([]string{"-w", "x > 1", "f.parquet", "--accent", "green", "--dim", "bright-black",
		"--border", "white", "--theme", "nord", "--no-sample", "--threads", "4",
		"--format", "a=3", "--format", " b = .2f ", "-w", "y < 2"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if o.Path != "f.parquet" || o.Where != "y < 2" || o.Accent != "green" || o.Dim != "bright-black" ||
		o.Border != "ansi_white" || o.Theme != "nord" || o.Sample == nil || *o.Sample || o.Threads != 4 {
		t.Errorf("options after the file: %+v", o)
	}
	if o.Formats["a"] != fmtx.ParseOverride("3") || o.Formats["b"] != fmtx.ParseOverride(".2f") {
		t.Errorf("formats: %+v", o.Formats)
	}
	o, _ = Parse([]string{"f", "--sample"}, env(nil))
	if o.Sample == nil || !*o.Sample {
		t.Error("--sample")
	}

	// the environment, when the option isn't given; invalid values are the defaults
	e := env(map[string]string{"PQX_ACCENT": "Magenta", "PQX_DIM": "BRIGHT-BLACK", "PQX_BORDER": "#123456"})
	o, _ = Parse([]string{"f"}, e)
	if o.Accent != "magenta" || o.Dim != "bright-black" || o.Border != "#123456" {
		t.Errorf("from the environment: %+v", o)
	}
	o, _ = Parse([]string{"f", "--accent", "cyan", "--dim", "faint", "--border", "red"}, e)
	if o.Accent != "cyan" || o.Dim != "faint" || o.Border != "ansi_red" {
		t.Errorf("options win over the environment: %+v", o)
	}
	o, _ = Parse([]string{"f"}, env(map[string]string{"PQX_ACCENT": "red", "PQX_DIM": "x"}))
	if o.Accent != "blue" || o.Dim != "faint" {
		t.Errorf("invalid environment: %+v", o)
	}

	// errors beyond Python's (its stylesheet failed to parse; its theme
	// error came from opening the app)
	for _, c := range []struct {
		args []string
		env  map[string]string
		want string
	}{
		{[]string{"f", "--theme", "monokai"}, nil,
			"argument --theme: invalid choice: 'monokai' (choose from tokyo-night, dracula, catppuccin-mocha, nord, gruvbox)"},
		{[]string{"f", "--border", "orange"}, nil, "argument --border: unknown colour"},
		{[]string{"f"}, map[string]string{"PQX_BORDER": "bogus"}, "PQX_BORDER: unknown colour"},
		{[]string{"f", "--border", "\x1b[31m"}, nil, `argument --border: unknown colour "ansi_\x1b[31m"`},
		{[]string{"\x1b]0;T\x07", "--bogus\x1b"}, nil, "unrecognized arguments: --bogus␛"},
	} {
		_, err := Parse(c.args, env(c.env))
		var ue *UsageError
		if !errors.As(err, &ue) || !strings.HasPrefix(ue.Msg, c.want) {
			t.Errorf("%q: %v, want %s", c.args, err, c.want)
		}
	}
}

func TestPyInt(t *testing.T) {
	for in, want := range map[string]int{
		"7": 7, " 7 ": 7, "+7": 7, "-7": -7, "1_000": 1000, "\u0663": 3, "\U0001d7d9": 1, "\u00a07\u2003": 7,
		"99999999999999999999": math.MaxInt, "-99999999999999999999": -math.MaxInt,
	} {
		if n, ok := pyInt(in); !ok || n != want {
			t.Errorf("pyInt(%q) = %d, %v; want %d", in, n, ok, want)
		}
	}
	for _, in := range []string{"", " ", "1.5", "1__0", "_1", "1_", "0x10", "1e3", "+-1", "--1", "\u00bd"} {
		if _, ok := pyInt(in); ok {
			t.Errorf("pyInt(%q) accepted", in)
		}
	}
}

func TestWidth(t *testing.T) {
	if w := Width(env(map[string]string{"COLUMNS": "100"}), nil); w != 98 {
		t.Errorf("COLUMNS=100: %d", w)
	}
	// $COLUMNS is read with Python's int()
	for _, c := range []string{" 60 ", "6_0", "\t60", "+60", "\u0666\u0660"} {
		if w := Width(env(map[string]string{"COLUMNS": c}), nil); w != 58 {
			t.Errorf("COLUMNS=%q: %d", c, w)
		}
	}
	for _, c := range []string{"60.0", "6__0", "_60", "-5", "0"} {
		if w := Width(env(map[string]string{"COLUMNS": c}), nil); w != 78 {
			t.Errorf("COLUMNS=%q: %d", c, w)
		}
	}
	if w := Width(env(map[string]string{"COLUMNS": "x"}), nil); w != 78 {
		t.Errorf("no terminal: %d", w)
	}
}
