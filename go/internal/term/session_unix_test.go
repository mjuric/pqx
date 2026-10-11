//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package term

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The suspend key is the terminal's VSUSP, unless signal keys are off or it
// is undefined.
func TestSuspChar(t *testing.T) {
	var tio unix.Termios
	tio.Lflag = unix.ISIG
	tio.Cc[unix.VSUSP] = 0x1a
	if c := suspChar(&tio); c != 0x1a {
		t.Errorf("^Z: %#x", c)
	}
	tio.Cc[unix.VSUSP] = 0x19
	if c := suspChar(&tio); c != 0x19 {
		t.Errorf("susp ^Y: %#x", c)
	}
	for _, v := range []uint8{0, 0xff} { // stty susp undef
		tio.Cc[unix.VSUSP] = v
		if c := suspChar(&tio); c != 0 {
			t.Errorf("susp %#x: %#x", v, c)
		}
	}
	tio.Cc[unix.VSUSP] = 0x1a
	tio.Lflag = 0 // stty -isig
	if c := suspChar(&tio); c != 0 {
		t.Errorf("-isig: %#x", c)
	}
}

// At the suspend key the terminal filter passes on what came before it,
// drops the rest of the read, and stops reading until Resume: later input
// stays in the terminal.
func TestFilterStopsAtSuspendKey(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	f, err := NewTTYFilter(pr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hit := make(chan struct{}, 4)
	keys, _ := suspKeys(0x1a)
	f.WatchSuspend(keys, func() { hit <- struct{}{} })
	out := make(chan string, 16)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				out <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	expect := func(want string) {
		t.Helper()
		got := ""
		for got != want {
			select {
			case s := <-out:
				got += s
			case <-time.After(2 * time.Second):
				t.Fatalf("read %q, want %q", got, want)
			}
		}
	}
	for _, key := range []string{"\x1a", "\x1b[122;5u", "\x1b[27;5;122~"} {
		if _, err := pw.WriteString("ab" + key + "q" + key + "j"); err != nil {
			t.Fatal(err)
		}
		expect("ab")
		select {
		case <-hit:
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: no suspend", key)
		}
		if !f.Paused() {
			t.Fatalf("%q: not paused", key)
		}
		// typed after the key: left in the terminal, not read
		if _, err := pw.WriteString("ls\r"); err != nil {
			t.Fatal(err)
		}
		select {
		case s := <-out:
			t.Fatalf("%q: read %q while paused", key, s)
		case <-hit:
			t.Fatalf("%q: a second suspend", key)
		case <-time.After(200 * time.Millisecond):
		}
		f.Pause() // as suspend does
		if err := f.Resume(); err != nil {
			t.Fatal(err)
		}
		expect("ls\r")
		if f.Paused() {
			t.Fatal("still paused after Resume")
		}
	}
}
