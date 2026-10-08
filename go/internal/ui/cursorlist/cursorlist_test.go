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
	if got := l.View(5, 3, plain); got != "a    \nb    \nc    " {
		t.Fatalf("view %q", got)
	}
	l.Highlight(4)
	if got := l.View(5, 3, plain); got != "c    \nd    \n[e  ]" {
		t.Fatalf("view %q", got)
	}
	if l.At(0) != 2 || l.At(3) != -1 {
		t.Fatalf("At %d %d", l.At(0), l.At(3))
	}
	l.Move(-10)
	if l.HighlightedID() != "a" || !strings.HasPrefix(l.View(5, 3, plain), "[a  ]") {
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
