package term

import (
	"os"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
)

// Session is pqx's hold on the terminal while the app runs: raw mode, the
// input filter, the output wrapper and the signals. Close (deferred in
// main, and called from main's panic handler) restores the terminal in
// every case.
type Session struct {
	out     *Output
	input   *Filter // nil where the filter isn't used (Windows)
	tty     *ttyState
	restore func() // raw mode off, the reader stopped, files closed
	once    sync.Once
	sig     atomic.Value // the os.Signal that ended the app

	exit func(code int) // os.Exit; ends pqx from a signal while suspended
}

var active atomic.Pointer[Session]

// ProgramOptions are the options that make Bubble Tea use the session's
// input and output; signals are the session's (Run).
func (s *Session) ProgramOptions() []tea.ProgramOption {
	var opts []tea.ProgramOption
	if s.tty != nil { // the session handles the signals
		opts = append(opts, tea.WithoutSignalHandler())
	}
	if s.input != nil {
		opts = append(opts, tea.WithInput(s.input.File()))
	}
	if s.out != nil {
		opts = append(opts, tea.WithOutput(s.out))
	}
	return opts
}

// Run runs the app on the session's terminal. The session handles the
// signals: SIGINT quits as q does; SIGTERM, SIGHUP and SIGQUIT stop the app
// (Signal says which); SIGTSTP suspends it, with the terminal restored,
// until SIGCONT. Options are added to the session's.
func (s *Session) Run(m tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	p := tea.NewProgram(m, append(s.ProgramOptions(), opts...)...)
	stop := s.handleSignals(p)
	defer stop()
	return p.Run()
}

// Signal is the signal that ended the app (Run), or nil.
func (s *Session) Signal() os.Signal {
	v, _ := s.sig.Load().(os.Signal)
	return v
}

// Close restores the terminal: if pqx is still in the alternate screen (it
// crashed) the screen is cleared and left; raw mode is turned off. It is
// safe to call more than once and from any goroutine.
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
// (not one Bubble Tea started, which recovers by itself) calls it from its
// panic handler before the process dies.
func RestoreTerminal() {
	if s := active.Load(); s != nil {
		s.Close()
	}
}

// Open starts a session on the process's terminal: input from stdin if it
// is one, otherwise the controlling terminal (as Bubble Tea does); output to
// stdout if it is a terminal, else stderr if that is one (as Textual draws
// on stderr), else the controlling terminal.
func Open() (*Session, error) {
	s, err := open(os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		active.Store(s)
	}
	return s, err
}
