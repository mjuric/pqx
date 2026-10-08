//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package term

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/unix"
)

var active atomic.Pointer[Session]

// open puts the input terminal into raw mode itself: Bubble Tea only does
// that for an input that is a terminal file, and the filter is not one.
// Output processing stays on: for an input that isn't a terminal file,
// Bubble Tea's renderer counts on the terminal turning "\n" into "\r\n"
// (ONLCR).
func open(stdin, stdout *os.File) (*Session, error) {
	in, closeIn := stdin, func() {}
	if !term.IsTerminal(in.Fd()) {
		tty, err := os.Open("/dev/tty")
		if err != nil {
			return nil, fmt.Errorf("could not open the terminal: %w", err)
		}
		in, closeIn = tty, func() { _ = tty.Close() }
	}
	state, err := term.MakeRaw(in.Fd())
	if err == nil {
		err = keepOutputProcessing(int(in.Fd()))
	}
	if err != nil {
		if state != nil {
			_ = term.Restore(in.Fd(), state)
		}
		closeIn()
		return nil, fmt.Errorf("could not put the terminal in raw mode: %w", err)
	}
	// a reader that Close can interrupt, so the filter's goroutine stops
	// reading the terminal when pqx is done with it
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		_ = term.Restore(in.Fd(), state)
		closeIn()
		return nil, err
	}
	s := &Session{out: NewOutput(stdout), input: NewFilter(cr)}
	s.restore = func() {
		cr.Cancel()
		_ = term.Restore(in.Fd(), state)
		closeIn()
	}
	return s, nil
}

// keepOutputProcessing turns output processing (OPOST) and "\n" to "\r\n"
// (ONLCR) back on after MakeRaw.
func keepOutputProcessing(fd int) error {
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	t.Oflag |= unix.OPOST | unix.ONLCR
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}
