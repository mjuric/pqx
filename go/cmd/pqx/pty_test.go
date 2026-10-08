//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui"
)

// These tests run this test binary as pqx (TestMain) in a pseudo-terminal,
// as test_terminal.py runs the real CLI.

type ptyApp struct {
	t    *testing.T
	cmd  *exec.Cmd
	ptm  *os.File
	mu   sync.Mutex
	out  bytes.Buffer
	done chan error
}

// startPty runs pqx with args in a 120×40 pseudo-terminal; mode is
// PQX_TEST_MAIN ("1" for pqx, "panic" for panicMain).
func startPty(t *testing.T, mode string, args ...string) *ptyApp {
	t.Helper()
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(ptm.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(ptm.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	pts, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(pts.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "PQX_TEST_MAIN="+mode, "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pts, pts, pts
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pts.Close()
	a := &ptyApp{t: t, cmd: cmd, ptm: ptm, done: make(chan error, 1)}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := ptm.Read(buf)
			a.mu.Lock()
			a.out.Write(buf[:n])
			a.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() { a.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		ptm.Close()
	})
	return a
}

func (a *ptyApp) output() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.out.String()
}

// waitFor waits until the output contains s.
func (a *ptyApp) waitFor(s string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if strings.Contains(a.output(), s) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (a *ptyApp) write(s string) {
	if _, err := a.ptm.WriteString(s); err != nil {
		a.t.Fatal(err)
	}
}

// exited waits up to d for pqx to exit.
func (a *ptyApp) exited(d time.Duration) (bool, error) {
	select {
	case err := <-a.done:
		return true, err
	case <-time.After(d):
		return false, nil
	}
}

func fixtureFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "wide.parquet")
	writeParquet(t, p, 30, 100)
	return p
}

// x10 is an X10 mouse report for button code b at 1-based (x, y).
func x10(b, x, y int) string {
	return "\x1b[M" + string([]byte{byte(32 + b), byte(32 + x), byte(32 + y)})
}

// checkCleared checks that the screen is cleared before the alternate
// screen is left (test_terminal.py's
// test_quit_clears_screen_before_leaving_alt_screen).
func checkCleared(t *testing.T, tail string) {
	t.Helper()
	leave := strings.LastIndex(tail, "\x1b[?1049l")
	if leave < 0 {
		t.Fatalf("pqx did not leave the alternate screen: %q", tail)
	}
	clear := strings.LastIndex(tail[:leave], "\x1b[H\x1b[2J")
	if clear < 0 {
		t.Errorf("no clear before leaving the alternate screen: %q", tail[max(0, leave-200):])
	}
}

func TestPtyQuitClearsScreen(t *testing.T) {
	a := startPty(t, "1", fixtureFile(t))
	if !a.waitFor("of 100", 20*time.Second) {
		t.Fatalf("no first frame: %q", a.output())
	}
	time.Sleep(300 * time.Millisecond)
	out := a.output()
	if !strings.Contains(out, "\x1b[?1006h") || strings.Contains(out, "1016") {
		t.Errorf("mouse modes: SGR on %v, 1016 touched %v",
			strings.Contains(out, "\x1b[?1006h"), strings.Contains(out, "1016"))
	}
	// the terminal turns the renderer's "\n" into "\r\n" (output processing
	// stays on in raw mode)
	for i := strings.IndexByte(out, '\n'); i >= 0; i = strings.IndexByte(out[i+1:], '\n') + i + 1 {
		if i == 0 || out[i-1] != '\r' {
			t.Fatalf("a bare newline reached the terminal: %q", out[max(0, i-40):min(len(out), i+40)])
		}
		if strings.IndexByte(out[i+1:], '\n') < 0 {
			break
		}
	}
	n := len(out)
	a.write("q")
	if ok, err := a.exited(5 * time.Second); !ok || err != nil {
		t.Fatalf("q didn't quit: %v %v", ok, err)
	}
	checkCleared(t, a.output()[n:])
}

// The split X10 report found with the prototype: a click at column 80 cut
// after ESC [ M used to type "q" and quit. Here it must click, and pqx
// keeps running.
func TestPtySplitX10ClickDoesNotQuit(t *testing.T) {
	a := startPty(t, "1", fixtureFile(t))
	if !a.waitFor("of 100", 20*time.Second) {
		t.Fatalf("no first frame: %q", a.output())
	}
	time.Sleep(300 * time.Millisecond)
	for _, c := range []struct{ x, y int }{{81, 9}, {97, 9}, {120, 10}} {
		r := x10(0, c.x, c.y) + x10(3, c.x, c.y)
		a.write(r[:3])
		time.Sleep(20 * time.Millisecond)
		a.write(r[3:])
		time.Sleep(200 * time.Millisecond)
	}
	if ok, _ := a.exited(500 * time.Millisecond); ok {
		t.Fatalf("pqx quit on a split mouse report")
	}
	n := len(a.output())
	a.write("q")
	if ok, err := a.exited(5 * time.Second); !ok || err != nil {
		t.Fatalf("q didn't quit: %v %v", ok, err)
	}
	checkCleared(t, a.output()[n:])
}

// panicky is the app with a key that panics, for the crash test.
type panicky struct{ tea.Model }

func (p panicky) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "P" {
		panic("test panic")
	}
	m, cmd := p.Model.Update(msg)
	return panicky{m}, cmd
}

// panicMain is pqx with panicky (PQX_TEST_MAIN=panic).
func panicMain(path string) int {
	ds, err := data.Open(path, data.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	defer ds.Close()
	code := runApp(panicky{ui.New(ds)}, os.Stderr)
	// the terminal is out of raw mode again: a newline is a newline
	fmt.Print("after\nexit\n")
	return code
}

func TestPtyCrashRestoresTerminal(t *testing.T) {
	a := startPty(t, "panic", fixtureFile(t))
	if !a.waitFor("of 100", 20*time.Second) {
		t.Fatalf("no first frame: %q", a.output())
	}
	time.Sleep(300 * time.Millisecond)
	n := len(a.output())
	a.write("P")
	ok, err := a.exited(5 * time.Second)
	if !ok {
		t.Fatal("pqx didn't exit after a panic")
	}
	if ee, isExit := err.(*exec.ExitError); !isExit || ee.ExitCode() != 1 {
		t.Errorf("exit: %v", err)
	}
	tail := a.output()[n:]
	checkCleared(t, tail)
	if !strings.Contains(tail, "test panic") {
		t.Errorf("the panic isn't reported: %q", tail)
	}
	// with the terminal's output processing back on, "\n" arrives as "\r\n"
	if !strings.Contains(tail, "after\r\nexit\r\n") {
		t.Errorf("raw mode not restored: %q", tail[max(0, len(tail)-100):])
	}
}
