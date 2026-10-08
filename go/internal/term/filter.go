// Package term sits between pqx and the terminal: an input filter for mouse
// reports Bubble Tea doesn't handle (X10 and urxvt, as GNU screen 4.x sends
// them) and for invalid UTF-8, the OSC 52 clipboard, and an output wrapper
// that clears the screen before pqx leaves the alternate screen, also after
// a crash. It is the port of Python pqx's _terminal.py.
package term

import (
	"bytes"
	"io"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// How long the filter waits for the rest of a sequence that was cut off at
// the end of a read. A lone ESC waits EscTimeout, the same as Bubble Tea's
// own escape timeout, and is then passed on as an Esc key Bubble Tea doesn't
// wait on, so Esc is as prompt as without the filter. Anything longer (the
// start of a mouse report, an incomplete UTF-8 character) is never typed by
// hand, so it can wait a little longer for a slow link.
const (
	EscTimeout = 50 * time.Millisecond
	SeqTimeout = 100 * time.Millisecond
)

const esc = 0x1b

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
	// escKey is the Esc key in the CSI u (fixterms) encoding, which Bubble
	// Tea decodes at once as the same key as a lone ESC.
	escKey = []byte("\x1b[27u")
)

// translator is the filter's state machine, without the reading and timing.
// feed takes bytes as read from the terminal and returns the bytes Bubble Tea
// should see; a sequence cut off at the end stays in buf until more input
// arrives or expire is called.
//
//   - X10 mouse reports (ESC [ M b x y, one byte each, 32 added) and urxvt
//     ones (ESC [ b ; x ; y M, decimal) become SGR reports
//     (ESC [ < b ; x ; y M, or m for a release). X10 doesn't say which
//     button was released: the one pressed last is used (x10_to_sgr in
//     Python pqx). Reports with a coordinate below 1 are dropped.
//   - Invalid UTF-8 is lenient: a byte from 0xA0 to 0xFF is the character
//     with that code (Latin-1), as Python pqx's decoder does. Bytes from 0x80
//     to 0x9F are dropped (Bubble Tea drops all invalid bytes): as characters
//     they would be C1 controls, which pqx never puts in its input boxes.
//   - Inside a bracketed paste nothing is translated but the UTF-8.
type translator struct {
	buf        []byte // input not yet passed on: a sequence cut off at the end
	lastButton int
	paste      bool
}

// feed adds p to the input and returns what can be passed on.
func (t *translator) feed(p []byte) []byte {
	t.buf = append(t.buf, p...)
	return t.process(false)
}

// expire passes on whatever is held, as it is: no more input is coming for
// it. A held lone ESC (or a pair, Alt+Esc) becomes an Esc key Bubble Tea
// doesn't wait on.
func (t *translator) expire() []byte {
	if !t.paste && (string(t.buf) == "\x1b" || string(t.buf) == "\x1b\x1b") {
		out := append(t.buf[:len(t.buf)-1:len(t.buf)-1], escKey...)
		t.buf = t.buf[:0]
		return out
	}
	return t.process(true)
}

// held reports how many bytes are held and how long to wait for the rest.
func (t *translator) held() (int, time.Duration) {
	if len(t.buf) > 0 && t.buf[len(t.buf)-1] == esc && len(t.buf) <= 2 {
		return len(t.buf), EscTimeout
	}
	return len(t.buf), SeqTimeout
}

func (t *translator) process(expired bool) []byte {
	buf := t.buf
	out := make([]byte, 0, len(buf)+16)
	i := 0
	loneEsc := -1 // position of an ESC passed on by itself, just before i
	for i < len(buf) {
		c := buf[i]
		if c == esc {
			n, emit, hold := t.escape(buf[i:], expired)
			if hold {
				// keep an ESC just before it with it: ESC ESC [ M … is Alt
				// and a mouse report, and Bubble Tea should see the two ESCs
				// together, not 50 ms apart
				if i > 0 && loneEsc == i-1 {
					i--
					out = out[:len(out)-1]
				}
				break
			}
			if n == 0 { // an ESC that starts nothing the filter knows
				out = append(out, esc)
				i++
				loneEsc = i - 1
				continue
			}
			out = append(out, emit...)
			i += n
			continue
		}
		if c < utf8.RuneSelf {
			out = append(out, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(buf[i:])
		if r != utf8.RuneError || size > 1 {
			out = append(out, buf[i:i+size]...)
			i += size
			continue
		}
		if !expired && !utf8.FullRune(buf[i:]) {
			break // a character cut off by the read: wait for the rest
		}
		if c >= 0xA0 {
			out = utf8.AppendRune(out, rune(c))
		}
		i++
	}
	t.buf = append(t.buf[:0], buf[i:]...)
	return out
}

// escape looks at input starting with ESC. It returns how many bytes it
// consumed and what to pass on for them, or hold if the input ends in the
// middle of something the filter would translate. n == 0 means the ESC is an
// ordinary one, passed on unchanged.
func (t *translator) escape(s []byte, expired bool) (n int, emit []byte, hold bool) {
	if t.paste {
		if bytes.HasPrefix(s, pasteEnd) {
			t.paste = false
			return len(pasteEnd), pasteEnd, false
		}
		if !expired && bytes.HasPrefix(pasteEnd, s) {
			return 0, nil, true
		}
		return 0, nil, false
	}
	if bytes.HasPrefix(s, pasteStart) {
		t.paste = true
		return len(pasteStart), pasteStart, false
	}
	n, b, x, y, partial := parseMouse(s)
	switch {
	case partial:
		return 0, nil, !expired
	case n == 0:
		return 0, nil, false
	}
	return n, t.sgr(b, x, y), false
}

// parseMouse parses an X10 or urxvt mouse report at the start of s (which
// starts with ESC). It returns the report's length and its button code and
// 1-based coordinates; n == 0 if s doesn't start with one, and partial if s
// ends before it can tell.
func parseMouse(s []byte) (n, b, x, y int, partial bool) {
	if len(s) < 3 {
		return 0, 0, 0, 0, len(s) == 1 || s[1] == '['
	}
	if s[1] != '[' {
		return
	}
	if s[2] == 'M' { // X10: three bytes, each the value plus 32
		for k := 3; k < 6; k++ {
			if k == len(s) {
				return 0, 0, 0, 0, true
			}
			if s[k] < 32 {
				return
			}
		}
		return 6, int(s[3]) - 32, int(s[4]) - 32, int(s[5]) - 32, false
	}
	// urxvt: ESC [ b ; x ; y M in decimal, with 32 added to b
	var v [3]int
	j := 2
	for g := 0; g < 3; g++ {
		start := j
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j-start > 6 {
			return // no such coordinate; not a report
		}
		if j == len(s) {
			return 0, 0, 0, 0, true
		}
		if j == start {
			return
		}
		v[g], _ = strconv.Atoi(string(s[start:j]))
		want := byte(';')
		if g == 2 {
			want = 'M'
		}
		if s[j] != want {
			return
		}
		j++
	}
	return j, v[0] - 32, v[1], v[2], false
}

// sgr is the SGR report for button code b at (x, y), as x10_to_sgr; nil
// drops the report.
func (t *translator) sgr(b, x, y int) []byte {
	if x < 1 || y < 1 || b < 0 {
		return nil
	}
	final := byte('M')
	switch {
	case b&64 != 0 || b&32 != 0: // wheel, or motion with or without a button
	case b&3 == 3: // release: X10 doesn't say which button, so the last one pressed
		b = t.lastButton | (b &^ 3)
		final = 'm'
	default:
		t.lastButton = b & 3
	}
	out := make([]byte, 0, 16)
	out = append(out, "\x1b[<"...)
	out = strconv.AppendInt(out, int64(b), 10)
	out = append(out, ';')
	out = strconv.AppendInt(out, int64(x), 10)
	out = append(out, ';')
	out = strconv.AppendInt(out, int64(y), 10)
	return append(out, final)
}

// Filter is an io.Reader for Bubble Tea's input (tea.WithInput) that reads
// the terminal and passes on its bytes translated as described on
// translator: X10 and urxvt mouse reports become SGR ones even when a read
// cuts them in two, and invalid UTF-8 is lenient. A sequence cut off at the
// end of a read waits for the rest for at most EscTimeout or SeqTimeout.
//
// Bubble Tea can't interrupt a Read on a reader that isn't a file; Close
// ends it.
type Filter struct {
	data  chan []byte
	err   error // the source's error; set before data is closed
	done  chan struct{}
	once  sync.Once
	t     translator
	out   []byte
	timer *time.Timer
}

// NewFilter starts reading src (the terminal, in raw mode) in a goroutine.
func NewFilter(src io.Reader) *Filter {
	f := &Filter{
		data:  make(chan []byte),
		done:  make(chan struct{}),
		timer: time.NewTimer(time.Hour),
	}
	f.timer.Stop()
	go f.pump(src)
	return f
}

func (f *Filter) pump(src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			select {
			case f.data <- append([]byte(nil), buf[:n]...):
			case <-f.done:
				return
			}
		}
		if err != nil {
			f.err = err
			close(f.data)
			return
		}
	}
}

// Read implements io.Reader. It is meant for one reader (Bubble Tea's input
// loop).
func (f *Filter) Read(p []byte) (int, error) {
	for len(f.out) == 0 {
		var expired <-chan time.Time
		if len(f.t.buf) > 0 {
			expired = f.timer.C
		}
		select {
		case d, ok := <-f.data:
			if !ok {
				f.out = f.t.expire()
				if len(f.out) == 0 {
					return 0, f.err
				}
				break
			}
			f.out = f.t.feed(d)
			if n, wait := f.t.held(); n > 0 {
				f.timer.Reset(wait)
			}
		case <-expired:
			f.out = f.t.expire()
		case <-f.done:
			return 0, io.EOF
		}
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	return n, nil
}

// Close stops the filter: a Read in progress and later ones return io.EOF.
// It doesn't close the source.
func (f *Filter) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}
