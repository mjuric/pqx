// Package kit is the contract between pqx's UI parts (docs/design/go-port.md,
// W0.1b): the shared state, the messages the parts send each other, the
// background-task registry, and the interfaces a pane and a dialog implement.
// The root model (internal/ui/app) owns the layout, focus, tabs, the dialog
// stack and the Esc rules; each part (grid, filter, detail, schema, meta,
// stats, plot, dialogs, chrome) lives in its own package and talks to the
// others only through this package.
//
// Work packages don't change this package; changes go through the
// integrator.
//
// Everything runs on Bubble Tea's one Update goroutine except the functions
// passed to Tasks.Run, which run on their own goroutines and must touch
// nothing but their arguments and the dataset.
package kit

import (
	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
)

// Tab is one of the five tabs.
type Tab int

const (
	TabData Tab = iota
	TabSchema
	TabStats
	TabPlot
	TabMeta
)

// TabNames are the tabs' names in the tab strip, in order.
var TabNames = []string{"Data", "Schema", "Stats", "Plot", "Meta"}

// Options are the command line's settings the UI needs.
type Options struct {
	Where    string // -w: a filter or a query to open with
	Sampling bool   // stats and plots sample (--sample, --no-sample, or automatic)
	Formats  map[string]fmtx.Override
	// SessionFormats are --format options: used, never saved.
	SessionFormats map[string]fmtx.Override
	Version        string
}

// SampleRows is how many rows stats and plots use when sampling.
const SampleRows = 2_000_000

// Look is the theme as the parts use it.
type Look interface {
	// Render turns styled text into terminal output (colour roles
	// "accent" and "border" resolved).
	Render(t styled.Text) string
	// Style is the style for a role: "accent", "dim", "border",
	// "border-focus", "error", "warning", "success", "header", "cursor"
	// (reverse video), "selection".
	Style(role string) styled.Style
	DarkBG() bool
}

// State is what the parts share. The root model owns it; parts read it
// freely and change it only where noted, announcing the change with the
// message named.
type State struct {
	// View is the grid's view and Columns its columns (the file's, or a
	// SQL query's); Total its row count, -1 while counting. Set by the
	// part that applies a view (filter, sort, "=", clear): ViewChangedMsg.
	View    data.View
	Columns []data.Column
	Total   int64

	// Current is the linked current column (Data, Detail, Schema and Stats
	// follow it; Plot doesn't): ColumnChangedMsg.
	Current string

	// Row is the grid's cursor row in the view, and FileRow its file row
	// (-1 for SQL views). Set by the grid: CursorMsg.
	Row, FileRow int64

	// Hidden columns and the number of pinned leftmost columns, by the
	// grid and the column picker: ColumnsChangedMsg.
	Hidden map[string]bool
	Pinned int

	// Formats are the column overrides in force (saved and session ones):
	// FormatChangedMsg.
	Formats map[string]fmtx.Override
	Raw     bool // "f": raw values everywhere (RawChangedMsg)

	Sampling bool // "m" (SamplingChangedMsg)

	// DetailOpen is whether the details pane shows (the root sets it).
	DetailOpen bool
}

// Sample is the Sample for stats and plots under the current setting.
func (s *State) Sample() data.Sample {
	if s.Sampling {
		return data.Sample{Rows: SampleRows}
	}
	return data.Sample{}
}

// Override is the override in force for a column.
func (s *State) Override(col string) fmtx.Override { return s.Formats[col] }

// Column is the column of the current view by name.
func (s *State) Column(name string) (data.Column, bool) {
	for _, c := range s.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return data.Column{}, false
}

// Env is what every part gets at construction.
type Env struct {
	DS    data.Dataset
	Opts  Options
	Look  Look
	State *State
	Tasks *Tasks
	// Dialogs makes dialogs; nil until WP10.
	Dialogs Dialogs
}

// KeyHint is one entry of the key bar: "/ filter".
type KeyHint struct{ Key, Help string }

// Pane is a part of the screen: the grid, the filter bar, the details pane,
// a tab's body.
type Pane interface {
	// Update gets every message the root broadcasts, and key, paste and
	// mouse messages only while the pane has focus (mouse messages with
	// coordinates relative to the pane's top left corner, and only when
	// they fall inside it).
	Update(msg tea.Msg) tea.Cmd
	// View draws the pane in exactly w × h cells.
	View(w, h int) string
	// Keys are the pane's key-bar hints while it has focus.
	Keys() []KeyHint
}

// Focusable panes are told when they gain and lose focus.
type Focusable interface {
	Focus() tea.Cmd
	Blur()
}

// Framed panes are drawn in a bordered panel; the root draws the border,
// the tab strip (for tab bodies) and these titles.
type Framed interface {
	// Title is drawn in the top border (after the tab strip, if any);
	// Subtitle in the bottom border (the grid's "‹ 11 · columns 12–21 of
	// 64 ›").
	Title() styled.Text
	Subtitle() styled.Text
}

// Inputs are panes with a text input; while one has focus the root doesn't
// take single-letter keys (x, e, m, ?, q, 1–5, …) as commands.
type Inputs interface {
	TypingFocused() bool
}

// Dialog is a modal over the screen. The root draws it centred (or at
// Position, if it implements Positioned) without moving what is behind it,
// gives it every key, paste and mouse message, and closes it on CloseDialogMsg.
type Dialog interface {
	Pane
	// Size is the dialog's size for a screen of w × h.
	Size(w, h int) (int, int)
}

// Positioned dialogs (the Plot drop-down) say where they go.
type Positioned interface {
	Position(w, h int) (x, y int)
}

// Cursored panes show the terminal's text cursor (an input with focus);
// the position is relative to the pane.
type Cursored interface {
	Cursor() *tea.Cursor
}

// Overlay is something drawn over the screen at (X, Y): a toast.
type Overlay struct {
	X, Y    int
	Content string
}

// Chrome is the title bar, the status line, the key bar and the toasts
// (WP10). It gets every broadcast message (NotifyMsg, StatusMsg,
// DoneMsg, …) through Update.
type Chrome interface {
	Update(msg tea.Msg) tea.Cmd
	TitleBar(w int) string
	// StatusLine is the Data tab's status line (other tabs draw their own).
	StatusLine(w int) string
	KeyBar(w int, hints []KeyHint) string
	Toasts(w, h int) []Overlay
}

// Dialogs makes the dialogs (WP10). Env.Dialogs may be nil until it is
// built; parts then show a notice instead. Each dialog sends its result as
// the message named and closes itself (CloseDialogMsg).
type Dialogs interface {
	// Goto asks for a row spec (1234, 1.5M, 50%, -1) of total rows (-1 while
	// counting): GotoMsg.
	Goto(total int64) Dialog
	// Format asks for col's format, starting from cur, checked against
	// sample: FormatSetMsg.
	Format(col data.Column, cur fmtx.Override, sample data.Value) Dialog
	// Columns picks the visible columns of cols (hidden marks the hidden
	// ones), landing on current: ColumnsPickedMsg.
	Columns(cols []data.Column, hidden map[string]bool, current string) Dialog
	// Export asks where and how to write the current view, then writes it
	// (task "export") and reports the result itself.
	Export() Dialog
	// Help shows the help.
	Help() Dialog
}

// GotoMsg: the go-to dialog's row (already resolved against the total).
type GotoMsg struct{ Row int64 }

// FormatSetMsg: the format dialog's result for Column (the zero Override
// resets it to automatic).
type FormatSetMsg struct {
	Column   string
	Override fmtx.Override
}

// ColumnsPickedMsg: the column picker's result, the columns to show.
type ColumnsPickedMsg struct{ Visible []string }
