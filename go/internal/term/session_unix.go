//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/unix"
)

// ttyState switches the input terminal between raw mode and the mode pqx
// found it in.
type ttyState struct {
	fd    uintptr
	saved *term.State
}

// raw puts the terminal in raw mode for input. Output processing stays on:
// for an input that isn't a terminal file, Bubble Tea's renderer counts on
// the terminal turning "\n" into "\r\n" (ONLCR).
func (t *ttyState) raw() error {
	if _, err := term.MakeRaw(t.fd); err != nil {
		return err
	}
	tio, err := unix.IoctlGetTermios(int(t.fd), ioctlGetTermios)
	if err != nil {
		return err
	}
	tio.Oflag |= unix.OPOST | unix.ONLCR
	return unix.IoctlSetTermios(int(t.fd), ioctlSetTermios, tio)
}

// cooked puts the terminal back as pqx found it.
func (t *ttyState) cooked() { _ = term.Restore(t.fd, t.saved) }

// open puts the input terminal into raw mode itself: Bubble Tea only does
// that for an input that is a terminal file, and the filter's pipe is not
// one.
func open(stdin, stdout, stderr *os.File) (*Session, error) {
	var closers []io.Closer
	closeAll := func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}
	in := stdin
	if !term.IsTerminal(in.Fd()) {
		tty, err := os.Open("/dev/tty")
		if err != nil {
			return nil, fmt.Errorf("could not open the terminal: %w", err)
		}
		in, closers = tty, append(closers, tty)
	}
	out := stdout
	switch {
	case term.IsTerminal(out.Fd()):
	case term.IsTerminal(stderr.Fd()):
		out = stderr
	default:
		tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("could not open the terminal: %w", err)
		}
		out, closers = tty, append(closers, tty)
	}
	saved, err := term.GetState(in.Fd())
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("could not read the terminal's mode: %w", err)
	}
	t := &ttyState{fd: in.Fd(), saved: saved}
	if err := t.raw(); err != nil {
		t.cooked()
		closeAll()
		return nil, fmt.Errorf("could not put the terminal in raw mode: %w", err)
	}
	// a reader that Close can interrupt, so the filter's goroutine stops
	// reading the terminal when pqx is done with it
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		t.cooked()
		closeAll()
		return nil, err
	}
	f, err := NewFilter(cr)
	if err != nil {
		cr.Cancel()
		t.cooked()
		closeAll()
		return nil, err
	}
	s := &Session{out: NewOutput(out), input: f, tty: t}
	s.restore = func() {
		cr.Cancel()
		t.cooked()
		closeAll()
	}
	return s, nil
}

// handleSignals handles the signals while p runs; the returned function
// stops it.
func (s *Session) handleSignals(p *tea.Program) func() {
	ch := make(chan os.Signal, 4)
	stopping := []os.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}
	signal.Notify(ch, append(stopping, syscall.SIGINT, syscall.SIGTSTP)...)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				switch sig {
				case syscall.SIGINT:
					p.Quit()
				case syscall.SIGTSTP:
					// run on Bubble Tea's event loop like an external
					// program: it releases the terminal, then restores and
					// repaints it when suspend returns
					p.Send(tea.Exec(suspend{s, ch}, nil)())
				default:
					s.sig.Store(sig)
					p.Kill()
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// suspend stops pqx with the terminal as pqx found it, and puts
// it back in raw mode after SIGCONT. Bubble Tea has left the alternate
// screen and stopped reading input before Run, and does the rest after.
type suspend struct {
	s  *Session
	ch chan os.Signal // the session's signal channel
}

func (c suspend) Run() error {
	if c.s.tty == nil {
		return errors.New("no terminal")
	}
	c.s.tty.cooked()
	cont := make(chan os.Signal, 1)
	signal.Notify(cont, syscall.SIGCONT)
	defer signal.Stop(cont)
	// Stop the process group as SIGTSTP would. SIGSTOP, because once Go
	// has handled SIGTSTP, signal.Reset doesn't give it back its default
	// action (a re-raised SIGTSTP is lost and pqx would wait here for ever);
	// SIGSTOP also stops an orphaned process group, where the kernel
	// discards a SIGTSTP stop. A shell says "Stopped (signal)".
	_ = syscall.Kill(0, syscall.SIGSTOP)
	<-cont
	return c.s.tty.raw()
}

func (suspend) SetStdin(io.Reader)  {}
func (suspend) SetStdout(io.Writer) {}
func (suspend) SetStderr(io.Writer) {}
