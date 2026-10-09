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

// present reports whether pqx still has its controlling terminal.
func (t *ttyState) present() bool {
	_, err := unix.IoctlGetInt(int(t.fd), unix.TIOCGPGRP)
	return err == nil
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
	f, err := NewTTYFilter(in)
	if err != nil {
		t.cooked()
		closeAll()
		return nil, err
	}
	s := &Session{out: NewOutput(out), input: f, tty: t, exit: os.Exit}
	s.restore = func() {
		_ = f.Close()
		t.cooked()
		closeAll()
	}
	return s, nil
}

// handleSignals handles the signals while p runs; the returned function
// stops it.
func (s *Session) handleSignals(p *tea.Program) func() {
	// one channel, so that a SIGCONT is seen in order after the SIGTSTP
	// it answers
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTSTP,
		syscall.SIGCONT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				switch sig {
				case syscall.SIGCONT: // not suspended: nothing to do
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

// orphaned reports whether pqx's process group is orphaned (POSIX): no
// member has a parent in another process group of the same session, as a
// job-control shell is. It follows pqx's ancestors while they are in its
// group (a wrapper script, make), so it can be wrong only for a group whose
// other members have their own parents; when an ancestor can't be read it
// says orphaned, which only means SIGTSTP is ignored.
func orphaned() bool {
	pg := unix.Getpgrp()
	sid, err := unix.Getsid(0)
	if err != nil {
		return true
	}
	pid := unix.Getpid()
	for range 64 {
		ppid, err := parentOf(pid)
		if err != nil || ppid <= 0 {
			return true
		}
		ppg, err1 := unix.Getpgid(ppid)
		psid, err2 := unix.Getsid(ppid)
		if err1 != nil || err2 != nil {
			return true
		}
		if ppg != pg {
			return psid != sid
		}
		pid = ppid
	}
	return true
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
	c.s.input.Pause()
	// Stopped as a job (the terminal's Ctrl+Z stops the whole job), pqx
	// may be in the background already, the shell having taken the
	// terminal and set its own mode: then the mode is the shell's business
	// (it gives the job its own back on fg).
	background := !t.foreground()
	if !background {
		t.cooked()
	}
	for {
		if background && t.foreground() {
			break // the shell continued pqx (fg) before it stopped itself
		}
		// Stop pqx itself, as SIGTSTP's default action would (only this
		// process: without job control its group holds its parent and
		// siblings, which must not be frozen). SIGSTOP, because once Go
		// has handled SIGTSTP, signal.Reset doesn't give it back its
		// default action (a re-raised SIGTSTP is lost and pqx would wait
		// here for ever). A shell says "Stopped (signal)".
		_ = syscall.Kill(unix.Getpid(), syscall.SIGSTOP)
		c.continued()
		if t.foreground() {
			break
		}
		// Continued in the background: by bg, or by kill %1, which sends
		// SIGTERM and then SIGCONT (Go may hand them over in either order,
		// so give a terminating signal a moment). Then stop again until fg,
		// rather than take the terminal back (Textual's _stop_again).
		background = true
		select {
		case sig := <-c.ch:
			c.handle(sig)
		case <-time.After(200 * time.Millisecond):
		}
		if orphaned() || !t.present() {
			// the shell is gone (the kernel's orphan rule came too early
			// to apply): nothing would continue pqx; end as on a hang-up
			c.handle(syscall.SIGHUP)
		}
	}
	if err := c.s.input.Resume(); err != nil {
		return err
	}
	return t.raw()
}

// continued waits for SIGCONT, reading the signals that come first; a
// terminating one ends pqx.
func (c suspend) continued() {
	for {
		sig := <-c.ch
		if sig == syscall.SIGCONT {
			return
		}
		c.handle(sig)
	}
}

// handle is a signal while suspended: a terminating one ends pqx.
func (c suspend) handle(sig os.Signal) {
	switch sig {
	case syscall.SIGTSTP, syscall.SIGCONT:
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
