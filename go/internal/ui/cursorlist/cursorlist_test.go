package cursorlist

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
)

func plain(t styled.Text) string {
	s := t.Plain
	for _, sp := range t.Spans {
		if sp.Style.Reverse {
			r := []rune(s)
			return string(r[:sp.Start]) + "[" + string(r[sp.Start:sp.End]) + "]" + string(r[sp.End:])
		}
	}
	return s
}

func TestList(t *testing.T) {
	var l List
	var items []Item
	for _, s := range []string{"a", "b", "c", "d", "e", "f"} {
		items = append(items, Item{ID: s, Text: styled.New(s, styled.Style{})})
	}
	l.SetItems(items)
	l.Width = 3
	if l.Highlighted() != -1 {
		t.Fatal("highlighted after SetItems")
	}
	if got := l.View(5, 3, plain); got != "a    \nb   ▄\nc    " { // with a scrollbar
		t.Fatalf("view %q", got)
	}
	l.Highlight(4)
	if got := l.View(5, 3, plain); got != "c    \nd    \n[e  ▄" { // (the test renderer's brackets take a cell)
		t.Fatalf("view %q", got)
	}
	if l.At(0) != 2 || l.At(3) != -1 {
		t.Fatalf("At %d %d", l.At(0), l.At(3))
	}
	l.Move(-10)
	if l.HighlightedID() != "a" || !strings.HasPrefix(l.View(5, 3, plain), "[a  ") {
		t.Fatal("move to top")
	}
	l.Scroll(10)
	if l.At(0) != 3 {
		t.Fatalf("scrolled to %d", l.At(0))
	}
	if used, moved := l.Key(keyEnd()); !used || !moved || l.HighlightedID() != "f" {
		t.Fatal("end")
	}
	if Fit("abc", 2) != "ab" || ansi.StringWidth(Fit("x", 4)) != 4 {
		t.Fatal("Fit")
	}
}

func keyEnd() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnd} }

// A highlight before the list is first drawn doesn't scroll it: the first
// View shows it from the top when it fits there (OptionList isn't laid out
// yet either).
func TestHighlightBeforeDrawn(t *testing.T) {
	var l List
	for _, s := range []string{"a", "b", "c", "d", "e", "f"} {
		l.SetItems(append(l.Items(), Item{ID: s, Text: styled.New(s, styled.Style{})}))
	}
	l.Highlight(1)
	if v := l.View(4, 3, plain); !strings.HasPrefix(v, "a") {
		t.Fatalf("view %q", v)
	}
	l.Highlight(5)
	if v := l.View(4, 3, plain); !strings.HasPrefix(v, "d") {
		t.Fatalf("view %q", v)
	}
}

// Items wrap as OptionList wraps its prompts (Rich's word wrap, long words
// folded): the Stats list's "midpointMjdTai_flag_degraded  bool" puts bool
// on the next line.
func TestItemsWrap(t *testing.T) {
	for _, c := range []struct {
		text  string
		lines []string
	}{
		{"midpointMjdTai_flag_degraded  bool", []string{"midpointMjdTai_flag_degraded", "bool"}},
		{"a_very_long_column_name_that_needs_two_lines_or_more_x  f64", []string{"a_very_long_column_name_that_nee", "ds_two_lines_or_more_x  f64"}},
		{"id                  i64", []string{"id                  i64"}},
	} {
		var got []string
		for _, l := range Wrap(styled.New(c.text, styled.Style{}), 32) {
			got = append(got, l.Plain)
		}
		if strings.Join(got, "|") != strings.Join(c.lines, "|") {
			t.Errorf("Wrap(%q) = %q, want %q", c.text, got, c.lines)
		}
	}
	// styles follow the text onto the next line
	tx := styled.Text{Plain: "ab cd"}
	tx.Spans = []styled.Span{{Start: 3, End: 5, Style: styled.Style{Bold: true}}}
	if ls := Wrap(tx, 3); len(ls) != 2 || ls[1].Plain != "cd" || ls[1].Spans[0].Start != 0 || ls[1].Spans[0].End != 2 {
		t.Errorf("styled wrap %+v", ls)
	}
	// a list of wrapped items: lines, highlight, At, scrolling
	var l List
	l.Width = 6
	l.SetItems([]Item{{ID: "a", Text: styled.New("a", styled.Style{})}, {ID: "long", Text: styled.New("long long", styled.Style{})}, {ID: "c", Text: styled.New("c", styled.Style{})}})
	if got := l.View(6, 4, plain); got != "a     \nlong  \nlong  \nc     " { // ("long " loses its space)
		t.Fatalf("view %q", got)
	}
	if l.At(1) != 1 || l.At(2) != 1 || l.At(3) != 2 {
		t.Errorf("At %d %d %d", l.At(1), l.At(2), l.At(3))
	}
	l.Highlight(1)
	if got := l.View(6, 2, plain); got != "[long▄\n[long▄" { // (both lines reversed; the brackets take cells)
		t.Errorf("highlighted wrapped item %q", got)
	}
}
