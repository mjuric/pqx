package detail

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/styled"
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

// Textual's scrollbar: a thumb proportional to the view, at the top and at
// the bottom of the range.
func TestScrollbar(t *testing.T) {
	bar := styled.Style{Fg: "border"}
	plain := func(ts []styled.Text) string {
		var b strings.Builder
		for _, x := range ts {
			if x.Style.Reverse && x.Plain == " " {
				b.WriteString("█")
			} else {
				b.WriteString(x.Plain)
			}
		}
		return b.String()
	}
	if s := plain(scrollbar(10, 10, 0, bar)); s != strings.Repeat(" ", 10) {
		t.Fatalf("no scrolling: %q", s)
	}
	if s := plain(scrollbar(10, 40, 0, bar)); !strings.HasPrefix(s, "██") || strings.Count(s, "█") > 3 {
		t.Fatalf("top: %q", s)
	}
	if s := plain(scrollbar(10, 40, 30, bar)); !strings.HasSuffix(strings.TrimRight(s, " "), "█") || s[0] != ' ' {
		t.Fatalf("bottom: %q", s)
	}
}
