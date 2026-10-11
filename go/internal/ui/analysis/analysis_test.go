package analysis

import (
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/styled"
)

func TestScope(t *testing.T) {
	for _, c := range []struct {
		v    data.View
		want string
	}{
		{data.View{}, "all rows"},
		{data.View{SQL: "select 1"}, "SQL result"},
		{data.View{Where: "a > 1"}, "where a > 1"},
		{data.View{OrderBy: []data.Sort{{Column: "a"}}}, "where "},
	} {
		if got := Scope(c.v); got != c.want {
			t.Errorf("Scope(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", -1234567: "-1,234,567", 20000: "20,000"} {
		if got := Commas(n); got != want {
			t.Errorf("Commas(%d) = %q, want %q", n, got, want)
		}
	}
}

// FmtFloat against Python's fmt._fmt_float(v, 15).
func TestFmtFloat(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{
		{0, "0"}, {94.53285, "94.53285"}, {1000499.5, "1000499.5"}, {1.0000000000000001e17, "1e+17"},
		{0.000123456, "1.23456e-04"}, {-2.5, "-2.5"}, {123456789.123456789, "123456789.123457"},
		{1.7000000000001e17, "1.7000000000001e+17"},
	} {
		if got := FmtFloat(c.v, 15); got != c.want {
			t.Errorf("FmtFloat(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// Wrap breaks lines as Rich does (checked with Text.wrap), keeping the
// spaces after the last word.
func TestWrap(t *testing.T) {
	var tx styled.Text
	tx.Append("name", styled.Style{Bold: true})
	tx.Append("   a long description of the column", styled.Style{Dim: true})
	lines := Wrap(tx, 20)
	var got []string
	for _, l := range lines {
		got = append(got, l.Plain)
		if len([]rune(l.Plain)) > 20 {
			t.Errorf("line too long: %q", l.Plain)
		}
	}
	if strings.Join(got, "|") != "name   a long |description of the |column" {
		t.Fatalf("wrapped %q", got)
	}
	if lines[0].Spans[0] != (styled.Span{Start: 0, End: 4, Style: styled.Style{Bold: true}}) {
		t.Fatalf("spans %+v", lines[0].Spans)
	}
	if l := Wrap(styled.New(strings.Repeat("x", 25), styled.Style{}), 10); len(l) != 3 || l[2].Plain != "xxxxx" {
		t.Fatalf("folded %+v", l)
	}
}
