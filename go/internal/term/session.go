package term

import (
	"fmt"
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

	// suspendKey carries the suspend key (Ctrl+Z) from the input filter and
	// the app to the signal handler, which suspends pqx; nil when the key is
	// off (stty susp undef, -isig) and on Windows. suspName is the key's
	// name, as Bubble Tea gives it ("ctrl+z").
	suspendKey chan struct{}
	suspName   string
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
// until SIGCONT. The terminal's suspend key (Ctrl+Z; raw mode turns the
// terminal's own handling off) suspends it as the terminal would, on Unix;
// the app never sees the key. Options are added to the session's.
func (s *Session) Run(m tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	if s.suspendKey != nil && s.suspName != "" {
		m = ctrlZ{m, s.suspendKey, s.suspName}
	}
	p := tea.NewProgram(m, append(s.ProgramOptions(), opts...)...)
	stop := s.handleSignals(p)
	defer stop()
	m, err := p.Run()
	if z, ok := m.(ctrlZ); ok {
		m = z.Model
	}
	return m, err
}

// ctrlZ is the app with the suspend key taken out of its keys, in every
// focus and dialog: the key asks the signal handler to suspend pqx. The
// input filter normally takes the key out first (WatchSuspend); this is for
// an encoding it didn't see (cut in two by a read).
type ctrlZ struct {
	tea.Model
	req  chan<- struct{}
	name string // the key, "ctrl+z"
}

func (z ctrlZ) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == z.name {
		select {
		case z.req <- struct{}{}:
		default: // one is pending already
		}
		return z, nil
	}
	m, cmd := z.Model.Update(msg)
	z.Model = m
	return z, cmd
}

// drain drops the suspend requests pending in ch.
func drain(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// suspKeys are the encodings of the suspend key c (a control character) and
// its name in Bubble Tea, "" if it has none pqx knows: c itself, and for
// Ctrl+letter the forms of the keyboard protocols pqx turns on (kitty's
// CSI u, xterm's modifyOtherKeys).
func suspKeys(c byte) ([][]byte, string) {
	keys := [][]byte{{c}}
	if c < 1 || c > 26 {
		return keys, ""
	}
	l := int('a' + c - 1)
	keys = append(keys, fmt.Appendf(nil, "\x1b[%d;5u", l), fmt.Appendf(nil, "\x1b[27;5;%d~", l))
	return keys, "ctrl+" + string(rune(l))
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
