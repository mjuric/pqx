package app

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Panel is one bordered panel of a tab body that has several (Paneled).
type Panel struct {
	X, Y, W, H int // the border box, relative to the body's top left corner
	// Title goes in the top border; the first panel's is the tab strip.
	Title styled.Text
	// Focused panels have the accent border while the pane has focus.
	Focused bool
	// Content is the inside: lines of W-4 cells (border and one cell of
	// padding on each side), H-2 of them or fewer.
	Content string
}

// Paneled is a tab body drawn as several bordered panels, as Python pqx's
// Schema (the table, the column's description below it) and Metadata (the
// file beside its row groups) are. The root draws the borders; mouse
// messages reach the pane with coordinates relative to the body's top left
// corner (the first panel's top left border corner when it starts there).
type Paneled interface {
	kit.Pane
	Panels(w, h int) []Panel
}

// InnerFocus is a pane whose panels Tab steps through before the root moves
// focus on (Metadata: the file, then the row groups). CycleFocus moves d
// panels (1 for Tab, -1 for Shift+Tab) and reports false, moving nothing,
// when that would step past the last or first panel.
type InnerFocus interface {
	CycleFocus(d int) bool
}

// panels draws a Paneled body w × h.
func (a *App) panels(p Paneled, w, h int, tabs styled.Text, focused bool) string {
	blank := strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", w)+"\n", h), "\n")
	layers := []*lipgloss.Layer{lipgloss.NewLayer(blank)}
	for i, pn := range p.Panels(w, h) {
		title := pn.Title
		if i == 0 {
			title = tabs
		}
		f := a.frame(pn.Content, pn.W, pn.H, title, styled.Text{}, focused && pn.Focused, p)
		layers = append(layers, lipgloss.NewLayer(f).X(pn.X).Y(pn.Y).Z(i+1))
	}
	c := lipgloss.NewCanvas(w, h)
	c.Compose(lipgloss.NewCompositor(layers...))
	return c.Render()
}
