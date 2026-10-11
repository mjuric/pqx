package config

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// TestPyYAMLDifferential: LoadFormats against Python pqx's load_formats on
// ~4,000 documents, and dump against PyYAML's text (testdata/yaml_cases.json,
// written by testdata/make_yaml_cases.py). Where Python fails (a YAML
// error, or a crash on what PyYAML builds), Go must fail too, so it never
// rewrites the file. Go also refuses YAML 1.1 line breaks (NEL, LS, PS)
// and tabs, which yaml.v3 would read differently.
func TestPyYAMLDifferential(t *testing.T) {
	b, err := os.ReadFile("testdata/yaml_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Dump []struct {
			Cols map[string]any `json:"cols"`
			Text string         `json:"text"`
		} `json:"dump"`
		Load []struct {
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Hex   string         `json:"hex"`
			Out   map[string]any `json:"out"`
			Error string         `json:"error"`
		} `json:"load"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	toOv := func(v any) fmtx.Override {
		if f, ok := v.(float64); ok {
			return digits(int(f))
		}
		return spec(v.(string))
	}
	for _, c := range cases.Dump {
		cols := map[string]fmtx.Override{}
		for k, v := range c.Cols {
			cols[k] = toOv(v)
		}
		if got := dump(cols); got != c.Text {
			t.Errorf("dump(%q):\n%q\nPyYAML:\n%q", c.Cols, got, c.Text)
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "formats.yaml")
	for _, c := range cases.Load {
		text := []byte(c.Text)
		if c.Hex != "" || c.Name != "" {
			text, _ = hex.DecodeString(c.Hex)
		}
		if err := os.WriteFile(p, text, 0o644); err != nil {
			t.Fatal(err)
		}
		got, gerr := LoadFormats(p)
		what := c.Name
		if what == "" {
			what = c.Text
		}
		switch {
		case c.Error != "":
			if gerr == nil {
				t.Errorf("%q: Python fails (%s), Go reads %v", what, c.Error, got)
			}
		case gerr != nil:
			if !strings.ContainsAny(string(text), "\u0085\u2028\u2029\t") {
				t.Errorf("%q: Python reads %v, Go fails: %v", what, c.Out, gerr)
			}
		default:
			want := map[string]fmtx.Override{}
			for k, v := range c.Out {
				want[k] = toOv(v)
			}
			if len(got) != len(want) {
				t.Errorf("%q: Go reads %v, Python %v", what, got, want)
				continue
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%q: Go reads %v, Python %v", what, got, want)
					break
				}
			}
		}
	}
}
