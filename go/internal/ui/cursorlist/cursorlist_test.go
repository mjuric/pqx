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

// Scrollbar against Textual's ScrollBarRender.render_bar (# a reversed
// blank, . a blank, n or r after a glyph: plain or reversed).
func TestScrollbar(t *testing.T) {
	for _, c := range []struct {
		size, virtual, window, pos int
		want                       string
	}{
		{31, 61, 31, 0, "###############▁r..............."},
		{31, 61, 31, 30, "...............▇n###############"},
		{31, 61, 31, 13, "......▄n###############▅r........"},
		{3, 6, 3, 1, "▄n#."},
		{10, 200, 10, 57, "..▃n▃r......"},
	} {
		var b strings.Builder
		for _, x := range Scrollbar(c.size, c.virtual, c.window, c.pos, "red") {
			switch {
			case x.Plain == " " && x.Style.Reverse:
				b.WriteString("#")
			case x.Plain == " ":
				b.WriteString(".")
			case x.Style.Reverse:
				b.WriteString(x.Plain + "r")
			default:
				b.WriteString(x.Plain + "n")
			}
		}
		if b.String() != c.want {
			t.Errorf("%+v: %q", c, b.String())
		}
	}
}

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
