package footer

import (
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/styled"
)

// Python's JSON(value, indent=2, highlight=False).text.plain (rich 15) for
// each input; "" where it raises.
var prettyCases = [][2]string{
	{`{"a": 1, "b": [1, 2.5, 1e16, 1e-5, 0.0001, -0.0], "c": {}, "d": [], "e": null, "f": true}`,
		"{\n  \"a\": 1,\n  \"b\": [\n    1,\n    2.5,\n    1e+16,\n    1e-05,\n    0.0001,\n    -0.0\n  ],\n  \"c\": {},\n  \"d\": [],\n  \"e\": null,\n  \"f\": true\n}"},
	{`{"a": "\u001b]0;T\u0007", "b": "\u009b31m", "c": "\u007f", "d": "caf\u00e9 \"q\" \\ \/"}`,
		"{\n  \"a\": \"\\u001b]0;T\\u0007\",\n  \"b\": \"\u009b31m\",\n  \"c\": \"\u007f\",\n  \"d\": \"café \\\"q\\\" \\\\ /\"\n}"},
	{`{"a": 1, "b": 2, "a": 3}`, "{\n  \"a\": 3,\n  \"b\": 2\n}"},
	{`123`, "123"},
	{`"str"`, `"str"`},
	{`[1, [2, [3]]]`, "[\n  1,\n  [\n    2,\n    [\n      3\n    ]\n  ]\n]"},
	{`{"big": 123456789012345678901234567890, "f": 1.5E300, "g": 12345678901234567.0, "h": 1E400}`,
		"{\n  \"big\": 123456789012345678901234567890,\n  \"f\": 1.5e+300,\n  \"g\": 1.2345678901234568e+16,\n  \"h\": Infinity\n}"},
	{`not json`, ""},
	{`{"a": 1} x`, ""},
	{``, ""},
}

func TestPrettyJSON(t *testing.T) {
	for _, c := range prettyCases {
		got, ok := PrettyJSON(c[0])
		if !ok {
			got = ""
		}
		if got != c[1] {
			t.Errorf("%s:\ngot  %q\nwant %q", c[0], got, c[1])
		}
	}
}

func TestPyRepr(t *testing.T) {
	for f, want := range map[float64]string{
		0: "0.0", 1: "1.0", 0.1: "0.1", 1e15: "1000000000000000.0", 1e16: "1e+16", 123.456: "123.456",
		1e-4: "0.0001", 1.5e-5: "1.5e-05", -2.5e-300: "-2.5e-300", 1.0 / 3: "0.3333333333333333",
	} {
		if got := PyRepr(f); got != want {
			t.Errorf("%v: got %q want %q", f, got, want)
		}
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", -1234567: "-1,234,567",
		8072338: "8,072,338"} {
		if got := Commas(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
	if got := CommasF(1009042.25); got != "1,009,042" {
		t.Errorf("%q", got)
	}
	if got := CommasF(2.5); got != "2" { // half to even, as Python
		t.Errorf("%q", got)
	}
}

func TestWrap(t *testing.T) {
	var tx styled.Text
	tx.Append("SSSource.parquet", styled.Style{Fg: "cyan"})
	tx.Append("   /a/b/c/", styled.Style{Dim: true})
	got := Wrap(tx, 18)
	if len(got) != 2 || got[0].Plain != "SSSource.parquet" || got[1].Plain != "/a/b/c/" {
		t.Fatalf("%+v", got)
	}
	if got[1].Spans[0].Style != (styled.Style{Dim: true}) || got[0].Spans[0].Style.Fg != "cyan" {
		t.Fatalf("styles %+v", got)
	}
	// long words are cut, newlines kept, empty lines kept
	got = Wrap(styled.New("abcdefghij\n\nxy z", styled.Style{}), 4)
	var plain []string
	for _, l := range got {
		plain = append(plain, l.Plain)
	}
	if strings.Join(plain, "|") != "abcd|efgh|ij||xy z" {
		t.Fatalf("%q", plain)
	}
	// in a table cell (Rich), the spaces at a break stay as far as they fit
	got = WrapCell(styled.New("aaa bbb ccc", styled.Style{}), 4)
	if len(got) != 3 || got[0].Plain != "aaa " || got[2].Plain != "ccc" {
		t.Fatalf("%+v", got)
	}
	got = Wrap(styled.New("aaa bbb ccc", styled.Style{}), 4)
	if len(got) != 3 || got[0].Plain != "aaa" {
		t.Fatalf("%+v", got)
	}
	// wide characters count two cells
	got = Wrap(styled.New("漢字漢字", styled.Style{}), 5)
	if len(got) != 2 || got[0].Plain != "漢字" {
		t.Fatalf("%+v", got)
	}
}

type plainLook struct{}

func (plainLook) Style(string) styled.Style   { return styled.Style{} }
func (plainLook) DarkBG() bool                { return true }
func (plainLook) Render(t styled.Text) string { return t.Plain }

func TestTable(t *testing.T) {
	tb := NewTable([]string{"#", "name"}, []bool{true, false})
	for i, n := range []string{"a", "bbbbbb", "c", "d", "e"} {
		tb.Add([]styled.Text{{Plain: string(rune('0' + i)), Justify: styled.Right}, styled.New(n, styled.Style{})})
	}
	// 6 lines in 3: a scrollbar on the right, its thumb 1.5 cells from the
	// top (Textual's render_bar: blank thumb, then "▄" for the half)
	lines := tb.View(plainLook{}, 12, 3, styled.Style{Reverse: true})
	want := []string{" #  name    ", " 0  a      ▄", " 1  bbbbbb  "}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", lines)
	}
	tb.Key("end", 2, 12)
	if tb.Cursor != 4 || tb.Top != 3 {
		t.Fatalf("cursor %d top %d", tb.Cursor, tb.Top)
	}
	tb.Key("up", 2, 12)
	tb.Key("up", 2, 12)
	if tb.Cursor != 2 || tb.Top != 2 {
		t.Fatalf("cursor %d top %d", tb.Cursor, tb.Top)
	}
	tb.ScrollBy(-5, 2)
	if tb.Top != 0 || tb.Cursor != 2 {
		t.Fatalf("wheel: cursor %d top %d", tb.Cursor, tb.Top)
	}
	tb.Key("right", 2, 5)
	if tb.X != 4 {
		t.Fatalf("x %d", tb.X)
	}
}

// markLook draws reverse cells as "R", bold ones as "B", dim ones as "d".
type markLook struct{ plainLook }

func (markLook) Render(t styled.Text) string {
	r := []rune(t.Plain)
	for i := range r {
		st := t.Style
		for _, sp := range t.Spans {
			if sp.Start <= i && i < sp.End {
				st = st.Plus(sp.Style)
			}
		}
		switch {
		case st.Reverse:
			r[i] = 'R'
		case st.Bold:
			r[i] = 'B'
		case st.Dim:
			r[i] = 'd'
		}
	}
	return string(r)
}

// As Textual's DataTable with pqx's styles: the header bold and the cursor
// row reverse across the whole width; a right-justified cell's padding in
// its style, a left-justified one's not.
func TestTableStyles(t *testing.T) {
	tb := NewTable([]string{"n", "s"}, []bool{true, false})
	d := styled.Style{Dim: true}
	tb.Add([]styled.Text{{Plain: "1", Style: d, Justify: styled.Right}, {Plain: "x", Style: d}},
		[]styled.Text{{Plain: "22", Style: d, Justify: styled.Right}, {Plain: "y", Style: d}})
	got := tb.View(markLook{}, 10, 3, styled.Style{Reverse: true})
	want := []string{"BBBBBBBBBB", "RRRRRRRRRR", " dd  d    "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}
