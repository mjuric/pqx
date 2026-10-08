package term

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// MaxCopy is the most bytes of text Copy puts on the clipboard. Terminals
// limit OSC 52 payloads (some to a few hundred kB) and a value can be a
// whole file's worth of text; past this the text is cut.
const MaxCopy = 1 << 20

// Copied is what Copy put on the clipboard.
type Copied struct {
	Text string
	// Sanitized is true when control or invisible characters were replaced
	// by visible symbols (the app says so, as Python pqx does).
	Sanitized bool
	// Cut is true when the text was longer than MaxCopy bytes.
	Cut bool
}

// Copy returns the command that puts text on the system clipboard with OSC
// 52, after sanitize has shown its control and invisible characters as
// visible symbols (the UI passes fmtx.Sanitize keeping tab and newline): an
// ESC pasted into a terminal could end a bracketed paste and run what
// follows.
func Copy(text string, sanitize func(string) string) (tea.Cmd, Copied) {
	s := sanitize(text)
	c := Copied{Sanitized: s != text}
	if len(s) > MaxCopy {
		n := MaxCopy
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s, c.Cut = s[:n], true
	}
	c.Text = s
	return tea.SetClipboard(s), c
}
