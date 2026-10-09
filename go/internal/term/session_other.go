//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package term

import (
	"os"

	tea "charm.land/bubbletea/v2"
)

// ttyState is unused here: Bubble Tea owns the console.
type ttyState struct{}

// open on Windows (and other systems) leaves the input to Bubble Tea, which
// reads the console's input records itself (the mouse problems the filter
// solves are those of GNU screen and old Unix terminals), and only wraps
// the output.
func open(_, stdout, _ *os.File) (*Session, error) {
	return &Session{out: NewOutput(stdout)}, nil
}

// handleSignals leaves the signals to Bubble Tea's defaults.
func (s *Session) handleSignals(p *tea.Program) func() { return func() {} }
