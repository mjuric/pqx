//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package term

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

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
	isRaw atomic.Bool
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
	t.isRaw.Store(true)
	return unix.IoctlSetTermios(int(t.fd), ioctlSetTermios, tio)
}

// cooked puts the terminal back as pqx found it. It does nothing if it is
// already: changing the mode from the background (suspended) would stop pqx
// with SIGTTOU.
func (t *ttyState) cooked() {
	if t.isRaw.Swap(false) {
		_ = term.Restore(t.fd, t.saved)
	}
}

// foreground reports whether pqx's process group owns the terminal.
func (t *ttyState) foreground() bool {
	pg, err := unix.IoctlGetInt(int(t.fd), unix.TIOCGPGRP)
	return err == nil && pg == unix.Getpgrp()
}

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
	s := &Session{out: NewOutput(out), input: f, tty: t, exit: os.Exit}
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
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTSTP)
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
					if orphaned() {
						// nothing would resume pqx (it leads its own
						// session: ssh -t host pqx f, setsid): ignore it,
						// as the kernel ignores SIGTSTP there
						continue
					}
					// run on Bubble Tea's event loop like an external
					// program: it releases the terminal, then restores and
					// repaints it when suspend returns. Meanwhile the
					// signals are suspend's.
					resumed := make(chan struct{})
					p.Send(tea.Exec(suspend{s, ch, resumed}, nil)())
					select {
					case <-resumed:
					case <-done:
						return
					}
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

// orphaned reports whether pqx's process group is orphaned: no parent in
// another process group of the same session (a job-control shell) to
// resume it.
func orphaned() bool {
	ppid := unix.Getppid()
	ppg, err1 := unix.Getpgid(ppid)
	psid, err2 := unix.Getsid(ppid)
	sid, err3 := unix.Getsid(0)
	if err1 != nil || err2 != nil || err3 != nil {
		return true
	}
	return ppg == unix.Getpgrp() || psid != sid
}

// suspend stops pqx with the terminal as pqx found it, and puts it back in
// raw mode once SIGCONT has made it the foreground job again. Bubble Tea
// has left the alternate screen and stopped reading input before Run, and
// does the rest after. Bubble Tea's event loop is blocked in Run, so a
// terminating signal meanwhile (kill %1 sends SIGTERM, then SIGCONT) ends
// pqx from here, with the terminal already restored.
type suspend struct {
	s       *Session
	ch      chan os.Signal // the session's signals, read here while suspended
	resumed chan struct{}  // closed when Run returns
}

func (c suspend) Run() error {
	defer close(c.resumed)
	t := c.s.tty
	t.cooked()
	cont := make(chan os.Signal, 1)
	signal.Notify(cont, syscall.SIGCONT)
	defer signal.Stop(cont)
	for {
		// Stop the process group as SIGTSTP would. SIGSTOP, because once
		// Go has handled SIGTSTP, signal.Reset doesn't give it back its
		// default action (a re-raised SIGTSTP is lost and pqx would wait
		// here for ever). A shell says "Stopped (signal)".
		_ = syscall.Kill(0, syscall.SIGSTOP)
		select {
		case <-cont:
		case sig := <-c.ch:
			c.handle(sig)
			continue
		}
		if t.foreground() {
			break
		}
		// Continued in the background: by bg, or by kill %1, which sends
		// SIGTERM and then SIGCONT (Go may hand them over in either order,
		// so give a terminating signal a moment). Then stop again until fg,
		// rather than take the terminal back (Textual's _stop_again).
		select {
		case sig := <-c.ch:
			c.handle(sig)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return t.raw()
}

// handle is a signal while suspended: a terminating one ends pqx.
func (c suspend) handle(sig os.Signal) {
	switch sig {
	case syscall.SIGTSTP:
	case syscall.SIGINT:
		c.s.Close()
		c.s.exit(0)
	default:
		c.s.sig.Store(sig)
		c.s.Close()
		c.s.exit(128 + int(sig.(syscall.Signal)))
	}
}

func (suspend) SetStdin(io.Reader)  {}
func (suspend) SetStdout(io.Writer) {}
func (suspend) SetStderr(io.Writer) {}
