package app

import (
	"github.com/mjuric/pqx/go/internal/ui/kit"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
)

// paneled is a two-panel tab body (Metadata's layout) with inner focus.
type paneled struct {
	pane
	inner int
}

func (p *paneled) Panels(w, h int) []kit.Panel {
	lw := (w - 1) / 2
	return []kit.Panel{
		{X: 0, Y: 0, W: lw, H: h, Focused: p.inner == 0, Content: "left"},
		{X: lw + 1, Y: 0, W: w - lw - 1, H: h, Focused: p.inner == 1, Title: styled.New("right", styled.Style{}),
			Content: "right"},
	}
}

func (p *paneled) CycleFocus(d int) bool {
	if d > 0 && p.inner == 0 {
		p.inner = 1
		return true
	}
	if d < 0 && p.inner == 1 {
		p.inner = 0
		return true
	}
	p.inner = 1 - p.inner
	return false
}

func TestPaneledTab(t *testing.T) {
	a, ps := setup(t)
	m := &paneled{pane: pane{name: "meta"}}
	a.p.Meta = m
	run(a, key("5"))
	s := screen(a)
	top := s[bodyTop]
	if !strings.HasPrefix(top, " ┌─ 1 Data ─ 2 Schema") || !strings.Contains(top, "┐ ┌─ right ─") {
		t.Fatalf("top border %q", top)
	}
	if row := s[bodyTop+1]; !strings.HasPrefix(row, " │ left") || !strings.Contains(row, "│ │ right") {
		t.Fatalf("row %q", row)
	}
	for i, l := range strings.Split(ansi.Strip(a.View().Content), "\n") {
		if w := ansi.StringWidth(l); w != 100 {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	// mouse: relative to the body's top left corner
	run(a, tea.MouseClickMsg{X: 60, Y: bodyTop + 2, Button: tea.MouseLeft})
	if len(m.mouse) != 1 || m.mouse[0].X != 60-margin || m.mouse[0].Y != 2 {
		t.Fatalf("mouse %v", m.mouse)
	}
	// Tab steps through the panels, then on to the filter
	run(a, key("tab"))
	if m.inner != 1 || a.focus != "meta" {
		t.Fatalf("inner %d focus %s", m.inner, a.focus)
	}
	run(a, key("tab"))
	if a.focus != "filter" || !ps["filter"].focused {
		t.Fatalf("focus %s", a.focus)
	}
	// clicking a tab name on the first panel's border still switches tabs
	x := 3 + strings.Index("1 Data ─ 2 Schema", "Schema")
	run(a, tea.MouseClickMsg{X: x, Y: bodyTop, Button: tea.MouseLeft})
	if a.tab != 1 {
		t.Fatalf("tab %d", a.tab)
	}
}
