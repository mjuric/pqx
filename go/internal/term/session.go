package term

import (
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// Session is pqx's hold on the terminal while the app runs: raw mode, the
// input filter and the output wrapper. Close (deferred in main, and called
// from main's panic handler) restores the terminal in every case.
type Session struct {
	out     *Output
	input   *Filter // nil where the filter isn't used (Windows)
	restore func()  // raw mode off, the reader stopped
	once    sync.Once
}

// ProgramOptions are the options that make Bubble Tea use the session's
// input and output.
func (s *Session) ProgramOptions() []tea.ProgramOption {
	var opts []tea.ProgramOption
	if s.input != nil {
		opts = append(opts, tea.WithInput(s.input))
	}
	if s.out != nil {
		opts = append(opts, tea.WithOutput(s.out))
	}
	return opts
}

// Close restores the terminal: if pqx is still in the alternate screen (it
// crashed) the screen is cleared and left; raw mode is turned off. It is
// safe to call more than once.
func (s *Session) Close() {
	s.once.Do(func() {
		if s.out != nil {
			s.out.Restore()
		}
		if s.input != nil {
			_ = s.input.Close()
		}
		if s.restore != nil {
			s.restore()
		}
		active.CompareAndSwap(s, nil)
	})
}

// RestoreTerminal closes the open session, if any. A goroutine of pqx's own
// (not one Bubble Tea started, which recovers by itself) can call it from its
// panic handler before the process dies.
func RestoreTerminal() {
	if s := active.Load(); s != nil {
		s.Close()
	}
}

// Open starts a session on the process's terminal: stdin if it is one,
// otherwise the controlling terminal (as Bubble Tea does), and stdout.
func Open() (*Session, error) {
	s, err := open(os.Stdin, os.Stdout)
	if err == nil {
		active.Store(s)
	}
	return s, err
}
