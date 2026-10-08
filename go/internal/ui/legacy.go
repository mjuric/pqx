package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Legacy runs the prototype's model as the root model's Data tab
// (app.Parts.Legacy) until the grid, filter and chrome are ported (WP7,
// WP10, WP11). It draws everything below the title bar itself.
type Legacy struct{ M *Model }

// Update implements kit.Pane.
func (l Legacy) Update(msg tea.Msg) tea.Cmd {
	_, cmd := l.M.Update(msg)
	return cmd
}

// View implements kit.Pane; the size comes from WindowSizeMsg.
func (l Legacy) View(w, h int) string { return l.M.Render() }

// Keys implements kit.Pane: the prototype draws its own key bar.
func (l Legacy) Keys() []kit.KeyHint { return nil }

// Cursor implements kit.Cursored.
func (l Legacy) Cursor() *tea.Cursor { return l.M.View().Cursor }

// Quitting reports whether the prototype asked to quit.
func (l Legacy) Quitting() bool { return l.M.quitting }
