//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"golang.org/x/sys/unix"
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
	return startPtyOut(t, mode, nil, args...)
}

// startPtyOut is startPty with stdout redirected to out (nil: the pty).
func startPtyOut(t *testing.T, mode string, out *os.File, args ...string) *ptyApp {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "PQX_TEST_MAIN="+mode)
	return startPtyCmdOut(t, cmd, out)
}

// startPtyCmd runs cmd in a 120×40 pseudo-terminal with env added.
func startPtyCmd(t *testing.T, cmd *exec.Cmd, env ...string) *ptyApp {
	t.Helper()
	cmd.Env = append(append(os.Environ(), "PQX_TEST_MAIN="), env...)
	return startPtyCmdOut(t, cmd, nil)
}

func startPtyCmdOut(t *testing.T, cmd *exec.Cmd, out *os.File) *ptyApp {
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
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pts, pts, pts
	if out != nil {
		cmd.Stdout = out
	}
	// its own session; killed if this test process dies
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: syscall.SIGKILL}
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
	// a watchdog, so that nothing this test started outlives it stopped
	watchdog := time.AfterFunc(2*time.Minute, func() { _ = cmd.Process.Kill() })
	t.Cleanup(func() {
		watchdog.Stop()
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
	if !a.waitFor("row 0", 20*time.Second) {
		t.Fatalf("no first frame: %q", a.output())
	}
	time.Sleep(300 * time.Millisecond)
	out := a.output()
	if !strings.Contains(out, "\x1b[?1006h") || strings.Contains(out, "?1016") {
		t.Errorf("mouse modes: SGR on %v, 1016 touched %v",
			strings.Contains(out, "\x1b[?1006h"), strings.Contains(out, "?1016"))
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
	if !a.waitFor("row 0", 20*time.Second) {
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

func TestPtyCrashRestoresTerminal(t *testing.T) {
	a := startPty(t, "panic", fixtureFile(t))
	if !a.waitFor("row 0", 20*time.Second) {
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
	a.checkCooked()
	if !strings.Contains(tail, "test panic") {
		t.Errorf("the panic isn't reported: %q", tail)
	}
	// with the terminal's output processing back on, "\n" arrives as "\r\n"
	if !strings.Contains(tail, "after\r\nexit\r\n") {
		t.Errorf("raw mode not restored: %q", tail[max(0, len(tail)-100):])
	}
}

// termios is the pty's terminal mode.
func (a *ptyApp) termios() *unix.Termios {
	tio, err := unix.IoctlGetTermios(int(a.ptm.Fd()), unix.TCGETS)
	if err != nil {
		a.t.Fatal(err)
	}
	return tio
}

// checkCooked checks that the terminal is out of raw mode (line editing and
// echo on, as a new pty has them).
func (a *ptyApp) checkCooked() {
	a.t.Helper()
	if l := a.termios().Lflag; l&unix.ICANON == 0 || l&unix.ECHO == 0 {
		a.t.Errorf("terminal left in raw mode: lflag %#x", l)
	}
}

func (a *ptyApp) checkRaw() {
	a.t.Helper()
	if l := a.termios().Lflag; l&(unix.ICANON|unix.ECHO) != 0 {
		a.t.Errorf("terminal not in raw mode: lflag %#x", l)
	}
}

// state is the process's state letter from /proc (T when stopped).
func (a *ptyApp) state() string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", a.cmd.Process.Pid))
	if err != nil {
		return "?"
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	return f[0]
}

func (a *ptyApp) started() {
	a.t.Helper()
	if !a.waitFor("row 0", 20*time.Second) {
		a.t.Fatalf("no first frame: %q", a.output())
	}
	time.Sleep(300 * time.Millisecond)
	a.checkRaw()
}

// With stdout redirected pqx draws on the terminal (stderr), as Python pqx
// does, and writes nothing to the file.
func TestPtyStdoutRedirected(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	a := startPtyOut(t, "1", f, fixtureFile(t))
	a.started()
	a.write("q")
	if ok, err := a.exited(5 * time.Second); !ok || err != nil {
		t.Fatalf("q didn't quit: %v %v", ok, err)
	}
	if st, _ := f.Stat(); st.Size() != 0 {
		t.Errorf("%d bytes written to stdout", st.Size())
	}
	a.checkCooked()
}

// startShell runs an interactive bash in a pty of its own (its own
// session), as a user's terminal would, and starts pqx in it as a job (the
// test binary as pqx, on a local fixture). It returns the shell and pqx's
// pid.
func startShell(t *testing.T) (*ptyApp, int) {
	t.Helper()
	return startShellWith(t, "sh -c 'exec $PQX'")
}

// startShellWith is startShell with the command line given, in which $PQX
// runs pqx.
func startShellWith(t *testing.T, line string) (*ptyApp, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	fixture, err := filepath.Abs("../../testdata/fixtures/demo.parquet")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("no fixture: %v", err)
	}
	a := startPtyCmd(t, exec.Command(bash, "--norc", "--noprofile", "-i"), "PS1=PROMPT$ ",
		fmt.Sprintf("PQX=env PQX_TEST_MAIN=1 %s %s", os.Args[0], fixture))
	session := a.cmd.Process.Pid // bash leads the pty's session
	killAll := func() {
		// everything left in that session (never this test's own)
		for _, p := range inSession(session) {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	}
	watchdog := time.AfterFunc(2*time.Minute, killAll)
	t.Cleanup(func() {
		watchdog.Stop()
		killAll()
	})
	if !a.waitFor("PROMPT$", 10*time.Second) {
		t.Fatalf("no prompt: %q", a.output())
	}
	a.write(line + "\n")
	if !a.waitFor("row 0", 20*time.Second) {
		t.Fatalf("no first frame: %q", a.output())
	}
	time.Sleep(300 * time.Millisecond)
	exe, _ := os.Executable()
	pid := 0
	for _, p := range inSession(session) {
		if l, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", p)); err == nil && l == exe {
			pid = p
		}
	}
	if pid == 0 {
		t.Fatal("pqx's process not found")
	}
	return a, pid
}

// inSession are the processes of session sid.
func inSession(sid int) []int {
	var pids []int
	ps, _ := filepath.Glob("/proc/[0-9]*")
	for _, p := range ps {
		n, err := strconv.Atoi(filepath.Base(p))
		if err != nil || n == os.Getpid() {
			continue
		}
		if s, err := unix.Getsid(n); err == nil && s == sid {
			pids = append(pids, n)
		}
	}
	return pids
}

// procState is a process's state letter ("" when it is gone or a zombie).
func procState(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	st := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))[0]
	if st == "Z" || st == "X" {
		return ""
	}
	return st
}

func waitProc(pid int, want string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if procState(pid) == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// SIGTSTP from outside (kill -TSTP) suspends pqx with the terminal
// restored; fg brings it back as it was; bg leaves it stopped until fg.
func TestShellSuspendAndResume(t *testing.T) {
	a, pid := startShell(t)
	for round := 0; round < 2; round++ {
		n := len(a.output())
		_ = syscall.Kill(pid, syscall.SIGTSTP)
		if !waitProc(pid, "T", 5*time.Second) || !a.waitFor("Stopped", 5*time.Second) {
			t.Fatalf("pqx didn't stop: state %q, %q", procState(pid), a.output()[n:])
		}
		tail := a.output()[n:]
		checkCleared(t, tail)
		for _, off := range []string{"\x1b[?1006l", "\x1b[?25h"} {
			if !strings.Contains(tail, off) {
				t.Errorf("%q not written on suspend: %q", off, tail)
			}
		}
		if round == 1 {
			// continued in the background, pqx stops again by itself
			m := len(a.output())
			a.write("bg\n")
			time.Sleep(500 * time.Millisecond)
			if st := procState(pid); st != "T" {
				t.Fatalf("pqx runs in the background: state %q", st)
			}
			// it stops before touching the terminal (not on SIGTTOU)
			a.write("\n") // bash reports the job's new state at the next prompt
			time.Sleep(300 * time.Millisecond)
			if tail := a.output()[m:]; strings.Contains(tail, "tty output") || strings.Contains(tail, "\x1b[?1049h") {
				t.Errorf("pqx reached for the terminal in the background: %q", tail)
			}
		}
		n = len(a.output())
		a.write("fg\n")
		time.Sleep(500 * time.Millisecond)
		tail = a.output()[n:]
		for _, on := range []string{"\x1b[?1049h", "\x1b[?1006h", "row 0"} {
			if !strings.Contains(tail, on) {
				t.Errorf("round %d: %q not written on resume: %q", round, on, tail)
			}
		}
		if procState(pid) == "T" {
			t.Fatal("pqx still stopped after fg")
		}
	}
	a.write("q")
	if !waitProc(pid, "", 5*time.Second) || !a.waitFor("PROMPT$ ", 5*time.Second) {
		t.Fatalf("q didn't quit: %q", procState(pid))
	}
}

// kill %1 on a suspended pqx (SIGTERM, then SIGCONT) ends it at once, with
// the terminal left as it was; so does kill -HUP (bash continues a stopped
// job after SIGTERM and SIGHUP; other signals wait until it is continued).
func TestShellKillSuspended(t *testing.T) {
	for _, how := range []string{"kill %1", "kill -HUP %1"} {
		a, pid := startShell(t)
		_ = syscall.Kill(pid, syscall.SIGTSTP)
		if !waitProc(pid, "T", 5*time.Second) || !a.waitFor("Stopped", 5*time.Second) {
			t.Fatalf("pqx didn't stop: state %q", procState(pid))
		}
		time.Sleep(100 * time.Millisecond)
		n := len(a.output())
		a.write(how + "\n")
		if !waitProc(pid, "", 3*time.Second) {
			st, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
			t.Fatalf("%s: pqx still there: state %q\n%s\n%q", how, procState(pid), st, a.output()[n:])
		}
		time.Sleep(200 * time.Millisecond)
		if tail := a.output()[n:]; strings.Contains(tail, "\x1b[?1049h") || strings.Contains(tail, "\x1b[?1006h") {
			t.Errorf("%s: pqx took the terminal back: %q", how, tail)
		}
		a.write("echo ALIVE\n")
		if !a.waitFor("ALIVE\r\n", 3*time.Second) {
			t.Errorf("%s: the shell isn't usable: %q", how, a.output()[n:])
		}
	}
}

// A pqx continued in the background whose shell then dies (before pqx
// stops itself again) has nothing to continue it: it ends, as on a
// hang-up, rather than stay stopped for ever.
func TestShellDiesWhileInBackground(t *testing.T) {
	for _, delay := range []time.Duration{20 * time.Millisecond, 80 * time.Millisecond, 150 * time.Millisecond} {
		a, pid := startShell(t)
		_ = syscall.Kill(pid, syscall.SIGTSTP)
		if !waitProc(pid, "T", 5*time.Second) || !a.waitFor("Stopped", 5*time.Second) {
			t.Fatalf("pqx didn't stop: state %q", procState(pid))
		}
		a.write("bg\n")
		time.Sleep(delay)
		_ = a.cmd.Process.Kill() // the shell
		if !waitProc(pid, "", 5*time.Second) {
			t.Errorf("shell killed %v after bg: pqx left in state %q", delay, procState(pid))
		}
	}
}

// pqx run by a script (no job control: its process group holds its
// parent and siblings) stops only itself on SIGTSTP; the script and its
// other children keep running.
func TestSuspendStopsOnlyPqx(t *testing.T) {
	a, pid := startShellWith(t, "sh -c 'sleep 600 & $PQX; kill $!'")
	pg, _ := unix.Getpgid(pid)
	if pg == unix.Getpgrp() {
		t.Fatal("pqx is in this test's process group")
	}
	var others []int
	for _, p := range inSession(a.cmd.Process.Pid) {
		if g, _ := unix.Getpgid(p); g == pg && p != pid {
			others = append(others, p) // the sh wrapper and its sleep
		}
	}
	if len(others) < 2 {
		t.Fatalf("expected sh and sleep beside pqx, found %v", others)
	}
	_ = syscall.Kill(pid, syscall.SIGTSTP) // pqx only, as kill -TSTP PID
	if !waitProc(pid, "T", 5*time.Second) {
		t.Fatalf("pqx didn't stop: state %q", procState(pid))
	}
	time.Sleep(200 * time.Millisecond)
	for _, p := range others {
		if st := procState(p); st == "T" {
			t.Errorf("process %d of pqx's group was stopped too", p)
		}
	}
	n := len(a.output())
	_ = syscall.Kill(pid, syscall.SIGCONT)
	time.Sleep(500 * time.Millisecond)
	if st := procState(pid); st == "T" || !strings.Contains(a.output()[n:], "\x1b[?1049h") {
		t.Fatalf("pqx didn't come back after SIGCONT: state %q", st)
	}
	a.write("q")
	if !waitProc(pid, "", 5*time.Second) || !a.waitFor("PROMPT$ ", 5*time.Second) {
		t.Fatalf("q didn't quit: %q", procState(pid))
	}
}

// pqx run by a wrapper script that doesn't exec it, stopped as a job (the
// terminal's Ctrl+Z stops the job's whole process group), suspends, and fg
// brings it back.
func TestShellWrapperSuspend(t *testing.T) {
	a, pid := startShellWith(t, "sh -c '$PQX; true'")
	pg, _ := unix.Getpgid(pid)
	if pg == unix.Getpgrp() || pg == pid {
		t.Fatalf("pqx's group %d: not a wrapper's job", pg)
	}
	_ = syscall.Kill(-pg, syscall.SIGTSTP) // the job's group, in the pty's own session
	if !waitProc(pid, "T", 5*time.Second) || !a.waitFor("Stopped", 5*time.Second) {
		t.Fatalf("pqx didn't stop: state %q, %q", procState(pid), a.output())
	}
	n := len(a.output())
	a.write("fg\n")
	time.Sleep(500 * time.Millisecond)
	if st := procState(pid); st == "T" || !strings.Contains(a.output()[n:], "row 0") {
		for _, q := range inSession(a.cmd.Process.Pid) {
			b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", q))
			w, _ := os.ReadFile(fmt.Sprintf("/proc/%d/wchan", q))
			c, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", q))
			for _, l := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(l, "State") || strings.HasPrefix(l, "ShdPnd") || strings.HasPrefix(l, "SigPnd") {
					t.Logf("%d %q %s %s", q, c[:min(len(c), 30)], l, w)
				}
			}
		}
		t.Fatalf("fg didn't bring pqx back: state %q, %q", st, a.output()[n:])
	}
	a.write("q")
	if !waitProc(pid, "", 5*time.Second) || !a.waitFor("PROMPT$ ", 5*time.Second) {
		t.Fatalf("q didn't quit: %q", procState(pid))
	}
}

// Continued in the background, pqx stops again before touching the
// terminal, even where SIGTTOU wouldn't stop it (ignored).
func TestShellBackgroundWithTTOUIgnored(t *testing.T) {
	a, pid := startShellWith(t, `sh -c 'trap "" TTOU; exec $PQX'`)
	_ = syscall.Kill(pid, syscall.SIGTSTP)
	if !waitProc(pid, "T", 5*time.Second) || !a.waitFor("Stopped", 5*time.Second) {
		t.Fatalf("pqx didn't stop: state %q", procState(pid))
	}
	n := len(a.output())
	a.write("bg\n")
	time.Sleep(700 * time.Millisecond)
	if st := procState(pid); st != "T" {
		t.Errorf("pqx runs in the background: state %q", st)
	}
	if tail := a.output()[n:]; strings.Contains(tail, "\x1b[?1049h") || strings.Contains(tail, "\x1b[?1006h") {
		t.Errorf("pqx drew in the background: %q", tail)
	}
	n = len(a.output())
	a.write("fg\n")
	time.Sleep(500 * time.Millisecond)
	if !strings.Contains(a.output()[n:], "row 0") {
		t.Fatalf("fg didn't bring pqx back: %q", a.output()[n:])
	}
	a.write("q")
	if !waitProc(pid, "", 5*time.Second) {
		t.Fatalf("q didn't quit: %q", procState(pid))
	}
}

// pqx leading its own session (ssh -t host pqx f, setsid) has no shell to
// resume it: it ignores SIGTSTP rather than stop for ever.
func TestPtyOrphanedIgnoresTSTP(t *testing.T) {
	a := startPty(t, "1", fixtureFile(t))
	a.started()
	_ = a.cmd.Process.Signal(syscall.SIGTSTP)
	time.Sleep(500 * time.Millisecond)
	if st := a.state(); st == "T" {
		t.Fatal("pqx stopped with nothing to resume it")
	}
	a.checkRaw()
	a.write("q")
	if ok, err := a.exited(5 * time.Second); !ok || err != nil {
		t.Fatalf("q didn't quit: %v %v", ok, err)
	}
}

// SIGINT quits quietly with 0; SIGTERM, SIGHUP and SIGQUIT restore the
// terminal and exit with 128 + the signal.
func TestPtySignals(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGINT, 0}, {syscall.SIGTERM, 143}, {syscall.SIGHUP, 129}, {syscall.SIGQUIT, 131}} {
		a := startPty(t, "1", fixtureFile(t))
		a.started()
		n := len(a.output())
		_ = a.cmd.Process.Signal(c.sig)
		ok, err := a.exited(5 * time.Second)
		if !ok {
			t.Fatalf("%v: pqx didn't exit", c.sig)
		}
		code := 0
		if ee, isExit := err.(*exec.ExitError); isExit {
			code = ee.ExitCode()
		} else if err != nil {
			t.Errorf("%v: %v", c.sig, err)
		}
		if code != c.code {
			t.Errorf("%v: exit %d, want %d", c.sig, code, c.code)
		}
		tail := a.output()[n:]
		checkCleared(t, tail)
		if strings.Contains(tail, "pqx:") {
			t.Errorf("%v: a message: %q", c.sig, tail)
		}
		a.checkCooked()
	}
}

// writeParquet writes a file of ncols int64 columns c00, c01, … with rows
// 0..nrows-1 (value row*100 + column).
func writeParquet(t testing.TB, path string, ncols, nrows int) {
	t.Helper()
	var fields []arrow.Field
	var cols []arrow.Array
	for c := 0; c < ncols; c++ {
		fields = append(fields, arrow.Field{Name: fmt.Sprintf("c%02d", c), Type: arrow.PrimitiveTypes.Int64})
		b := array.NewInt64Builder(memory.DefaultAllocator)
		for r := 0; r < nrows; r++ {
			b.Append(int64(r*100 + c))
		}
		cols = append(cols, b.NewArray())
		b.Release()
	}
	sc := arrow.NewSchema(fields, nil)
	rec := array.NewRecordBatch(sc, cols, int64(nrows))
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pqarrow.WriteTable(tbl, f, 1<<20, nil, pqarrow.DefaultWriterProps()); err != nil {
		t.Fatal(err)
	}
}

// Column formats saved in formats.yaml apply from the first screen; a
// file that can't be read is ignored with a notice, and pqx starts.
func TestPtySavedFormats(t *testing.T) {
	fixture, err := filepath.Abs("../../testdata/fixtures/demo.parquet")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ yaml, want string }{
		{"columns:\n  ra: .2f\n", "333.15 "},
		{"columns:\n  ra: [unclosed\n", "Ignoring saved column formats"},
	} {
		cfg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cfg, "pqx"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg, "pqx", "formats.yaml"), []byte(c.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], fixture)
		a := startPtyCmd(t, cmd, "PQX_TEST_MAIN=1", "XDG_CONFIG_HOME="+cfg)
		if !a.waitFor(c.want, 20*time.Second) {
			t.Errorf("%q: no %q on the screen: %q", c.yaml, c.want, a.output())
		}
		if !a.waitFor("diaSourceId", 10*time.Second) {
			t.Errorf("%q: pqx didn't start", c.yaml)
		}
		a.write("q")
		if ok, err := a.exited(5 * time.Second); !ok || err != nil {
			t.Errorf("q didn't quit: %v %v", ok, err)
		}
	}
}
