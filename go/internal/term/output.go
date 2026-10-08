package term

import (
	"bytes"
	"os"
	"sync"
)

var (
	enterAlt = []byte("\x1b[?1049h")
	leaveAlt = []byte("\x1b[?1049l")
	// ClearScreen resets attributes, homes the cursor and clears the screen,
	// as Python pqx writes it before leaving the alternate screen.
	ClearScreen = []byte("\x1b[0m\x1b[H\x1b[2J")
	// restoreModes undoes what a running pqx may have turned on: the text
	// cursor hidden, mouse reporting, bracketed paste.
	restoreModes = []byte("\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l")
)

// Output is the terminal for Bubble Tea's output (tea.WithOutput). It clears
// the screen just before each switch back from the alternate screen: if the
// terminal or multiplexer ignores the alternate screen (some ssh and
// container setups, GNU screen or tmux with it turned off), pqx would
// otherwise leave its last frame behind with the shell prompt in the middle
// of it. Where the alternate screen works the clear is invisible.
//
// It is a term.File (Fd is the terminal's), so Bubble Tea still reads the
// window size and the colour profile from it.
type Output struct {
	f   *os.File
	mu  sync.Mutex
	alt bool // in the alternate screen
}

// NewOutput wraps f (normally os.Stdout).
func NewOutput(f *os.File) *Output { return &Output{f: f} }

// Fd, Read and Close are f's; they make Output a term.File. (The file isn't
// embedded: its WriteString and ReadFrom would bypass Write.)
func (o *Output) Fd() uintptr                { return o.f.Fd() }
func (o *Output) Read(p []byte) (int, error) { return o.f.Read(p) }
func (o *Output) Close() error               { return o.f.Close() }

// Write implements io.Writer.
func (o *Output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	q := p
	if bytes.Contains(p, leaveAlt) {
		q = bytes.ReplaceAll(p, leaveAlt, append(append([]byte(nil), ClearScreen...), leaveAlt...))
	}
	if i, j := bytes.LastIndex(p, enterAlt), bytes.LastIndex(p, leaveAlt); i >= 0 || j >= 0 {
		o.alt = i > j
	}
	if _, err := o.f.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}

// InAltScreen reports whether the last switch written was into the
// alternate screen.
func (o *Output) InAltScreen() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.alt
}

// Restore puts the terminal back after pqx stopped without leaving the
// alternate screen (a crash): it clears the screen, leaves the alternate
// screen, shows the cursor and turns mouse reporting and bracketed paste
// off. After a normal exit it writes nothing.
func (o *Output) Restore() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.alt {
		return
	}
	var b bytes.Buffer
	b.Write(ClearScreen)
	b.Write(leaveAlt)
	b.Write(restoreModes)
	_, _ = o.f.Write(b.Bytes())
	o.alt = false
}
