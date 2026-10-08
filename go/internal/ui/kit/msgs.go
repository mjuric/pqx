package kit

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
)

// Messages the parts send each other through the root, which broadcasts
// them to every part (the parts check whether they care). A part sends one
// by returning a tea.Cmd that yields it: kit.Send(msg).

// Send is a command yielding msg.
func Send(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// ViewChangedMsg: State.View, Columns and Total changed (a new filter, sort,
// query, or a cleared one). Parts showing view-dependent things refresh.
type ViewChangedMsg struct{}

// TotalMsg: State.Total became known (or changed).
type TotalMsg struct{}

// ColumnChangedMsg: State.Current changed; From names the part that changed
// it, so it doesn't react to its own change.
type ColumnChangedMsg struct{ From string }

// CursorMsg: State.Row and FileRow changed.
type CursorMsg struct{}

// ColumnsChangedMsg: State.Hidden or Pinned changed.
type ColumnsChangedMsg struct{}

// FormatChangedMsg: State.Formats changed for Column.
type FormatChangedMsg struct{ Column string }

// RawChangedMsg: State.Raw changed.
type RawChangedMsg struct{}

// SamplingChangedMsg: State.Sampling changed.
type SamplingChangedMsg struct{}

// Severity of a notice.
type Severity int

const (
	Info Severity = iota
	Success
	Warning
	Error
)

// NotifyMsg asks for a toast (Python's notify). Title and Text are shown as
// given: sanitize anything from the file first (fmtx.Sanitize).
type NotifyMsg struct {
	Severity Severity
	Title    string
	Text     string
	Timeout  time.Duration // 0 for the default (3 s; 8 s for errors)
}

// Notify is a command yielding a NotifyMsg.
func Notify(sev Severity, text string) tea.Cmd {
	return Send(NotifyMsg{Severity: sev, Text: text})
}

// StatusMsg sets the status line's message (the part after the row
// counts), replacing the previous one; an empty Text clears it.
type StatusMsg struct {
	Severity Severity
	Text     string
}

// OpenDialogMsg pushes a dialog; CloseDialogMsg pops the top one.
type OpenDialogMsg struct{ Dialog Dialog }
type CloseDialogMsg struct{}

// SwitchTabMsg shows a tab.
type SwitchTabMsg struct{ Tab Tab }

// FocusMsg asks the root to focus a part by name: "grid", "filter",
// "detail", or a tab body ("schema", "meta", …).
type FocusMsg struct{ Pane string }

// ToggleDetailMsg opens or closes the details pane.
type ToggleDetailMsg struct{}

// SetViewMsg asks the filter part to apply a view (from "=", sort, clear,
// -w), keeping the cursor on file row KeepFileRow if it is >= 0. Keys
// lists cell keys typed while the record is looked up, to replay on it.
type SetViewMsg struct {
	View        data.View
	KeepFileRow int64
	Keys        []tea.KeyPressMsg
}

// ColumnStatsMsg: "i" — show the Stats tab on Column.
type ColumnStatsMsg struct{ Column string }

// CopyMsg asks the root to copy Text to the clipboard (OSC 52); Text is
// sanitized first by the root.
type CopyMsg struct{ Text string }
