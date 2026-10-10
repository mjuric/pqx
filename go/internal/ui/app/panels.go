package app

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// panels draws a kit.Paneled body w × h.
func (a *App) panels(p kit.Paneled, w, h int, tabs styled.Text, focused bool) string {
	blank := strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", w)+"\n", h), "\n")
	layers := []*lipgloss.Layer{lipgloss.NewLayer(blank)}
	for i, pn := range p.Panels(w, h) {
		title := pn.Title
		if i == 0 {
			title = tabs
		}
		f := a.frame(pn.Content, pn.W, pn.H, title, styled.Text{}, focused && pn.Focused, p)
		// the focused table (Schema's, Metadata's row groups) is tinted under
		// a named theme; set relative to the body, moved by the caller
		if focused && pn.Focused && (p == a.p.Schema || (p == a.p.Meta && i == 1)) {
			a.tint = image.Rect(pn.X+2, pn.Y+1, pn.X+pn.W-2, pn.Y+pn.H-1)
			if p == a.p.Meta {
				a.tint.Max.Y -= 2 // the row groups' status line, below the table
			}
		}
		layers = append(layers, lipgloss.NewLayer(f).X(pn.X).Y(pn.Y).Z(i+1))
	}
	c := lipgloss.NewCanvas(w, h)
	c.Compose(lipgloss.NewCompositor(layers...))
	return c.Render()
}
