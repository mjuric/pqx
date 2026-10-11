package detail

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/sqllit"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/scrollbar"
)

// Port of tests/test_app.py::test_detail_entry_layout_matches_rich_grid: the
// entries of testdata/entries.json are laid out by Python pqx's EntryGrid
// (Rich), on random names and values with wide characters, units and
// derived readings, at random widths; the Go layout gives the same lines,
// and the quick one-line height agrees with Python's.
func TestEntryLayoutMatchesRich(t *testing.T) {
	b, err := os.ReadFile("testdata/entries.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Width, NW int
		Name      string
		Value     string
		Lines     []string
		OneLine   bool `json:"one_line"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	quick := 0
	for i, c := range cases {
		v := styled.Text{Plain: c.Value}
		got := entryLines(c.Name, v, c.NW, c.Width, styled.Style{Bold: true}, false)
		var lines []string
		for _, l := range got {
			lines = append(lines, l.Plain)
		}
		if strings.Join(lines, "|\n") != strings.Join(c.Lines, "|\n") {
			t.Fatalf("case %d (width %d, name width %d, %q / %q):\n got  %q\n want %q", i, c.Width, c.NW, c.Name, c.Value, lines, c.Lines)
		}
		if h := entryHeight(v, c.NW, c.Width); h != len(c.Lines) {
			t.Fatalf("case %d: height %d, want %d", i, h, len(c.Lines))
		}
		if c.OneLine {
			quick++
		}
	}
	if len(cases) < 300 || quick < 50 {
		t.Fatalf("%d cases, %d one-line", len(cases), quick)
	}
}

func TestEntryStyles(t *testing.T) {
	v := styled.New("12.5", styled.Style{})
	v.Append("  deg", styled.Style{Dim: true})
	v.Append("\n· 00h50m00.000s", styled.Style{Dim: true})
	// unfocused selection: the name (padded to its column) reversed
	l := entryLines("ra", v, 6, 30, styled.Style{Bold: true, Reverse: true}, false)
	if len(l) != 2 || l[0].Plain != "ra      12.5  deg             " || l[1].Plain != "        · 00h50m00.000s       " {
		t.Fatalf("%v", l)
	}
	if l[0].Spans[0] != (styled.Span{Start: 0, End: 6, Style: styled.Style{Bold: true, Reverse: true}}) {
		t.Fatalf("name spans %v", l[0].Spans)
	}
	// focused: every cell reversed
	l = entryLines("ra", v, 6, 30, styled.Style{Bold: true}, true)
	for _, x := range l {
		last := x.Spans[len(x.Spans)-1]
		if last.Start != 0 || last.End != 30 || !last.Style.Reverse {
			t.Fatalf("not reversed whole: %v", x.Spans)
		}
	}
}

// Textual's scrollbar (testdata/scrollbar.json, from ScrollBarRender.render_bar):
// the same glyph, reverse video and bar colour on every row.
func TestScrollbarMatchesTextual(t *testing.T) {
	b, err := os.ReadFile("testdata/scrollbar.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Size, Total, Top int
		Rows             [][3]any
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	bar := styled.Style{Fg: "border"}
	for i, c := range cases {
		got := scrollbar.Vertical(c.Size, c.Total, c.Top, bar, "")
		for y, r := range c.Rows {
			g := got[y]
			if g.Plain != r[0].(string) || g.Style.Reverse != r[1].(bool) || (g.Style.Fg == "border") != r[2].(bool) {
				t.Fatalf("case %d (%d rows, %d lines from %d) row %d: %q %+v, want %v", i, c.Size, c.Total, c.Top, y, g.Plain, g.Style, r)
			}
		}
	}
	if s := scrollbar.Vertical(10, 10, 0, bar, ""); s[0].Plain != " " || s[0].Style.Reverse {
		t.Fatal("a bar with nothing to scroll")
	}
}

// A value exactly as wide as the value column takes one line.
func TestExactFit(t *testing.T) {
	v := styled.Text{Plain: strings.Repeat("x", 24)}
	if h := entryHeight(v, 22, 48); h != 1 {
		t.Fatal(h)
	}
	if h := entryHeight(styled.Text{Plain: strings.Repeat("x", 25)}, 22, 48); h != 2 {
		t.Fatal(h)
	}
	if h := entryHeight(styled.Text{Plain: strings.Repeat("❤️", 12)}, 22, 48); h != 1 {
		t.Fatal(h)
	}
	if h := entryHeight(styled.Text{Plain: strings.Repeat("❤️", 30)}, 22, 48); h != 3 {
		t.Fatal(h)
	}
}

// Wrapping keeps every character, in order, and makes lines of exactly the
// width asked for.
func FuzzWrap(f *testing.F) {
	for _, s := range []string{"❤️❤️❤️ 👍🏽 a", "👨\u200d👩\u200d👧 x\u200by", "日本 e\u0301 \tz", "  a  b  ", "a\ufe0f\u200db"} {
		f.Add(s, 5)
	}
	f.Fuzz(func(t *testing.T, s string, width int) {
		width = 1 + (width%40+40)%40
		if !utf8.ValidString(s) || strings.ContainsAny(s, "\n\r") || sqllit.HasControls(s) || strayJoiner(s) {
			return
		}
		lines := wrapText(styled.Text{Plain: s}, width)
		var all strings.Builder
		for _, l := range lines {
			if w := cellLen(l.Plain); w != width && !(width == 1 && w == 0) {
				t.Fatalf("%q at %d: line %q is %d wide", s, width, l.Plain, w)
			}
			all.WriteString(l.Plain)
		}
		squash := func(x string) string {
			return strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, x)
		}
		// (a grapheme wider than the line is cut: only then may text go)
		if got, want := squash(all.String()), squash(expandTabs(styled.Text{Plain: s}).Plain); got != want && width > 2 {
			t.Fatalf("%q at %d: %q", s, width, all.String())
		}
	})
}

// strayJoiner reports a zero-width joiner or variation selector without a
// character to join to: Rich's widths for those are nonsense (a joiner takes
// the space after it along), and so are the Go port's.
func strayJoiner(s string) bool {
	rs := []rune(s)
	for i, r := range rs {
		if r != 0x200D && r != 0xFE0F {
			continue
		}
		if i == 0 || unicode.IsSpace(rs[i-1]) || (r == 0x200D && (i == len(rs)-1 || unicode.IsSpace(rs[i+1]))) {
			return true
		}
	}
	return false
}
