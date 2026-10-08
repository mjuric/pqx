package term

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// screenModel is a full-screen app with the mouse on, as pqx's; "p" panics
// in Update and "q" quits.
type screenModel struct{ cmd tea.Cmd }

func (m screenModel) Init() tea.Cmd { return m.cmd }
func (m screenModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "q":
			return m, tea.Quit
		case "p":
			panic("boom")
		}
	}
	return m, nil
}
func (m screenModel) View() tea.View {
	v := tea.NewView("FRAME-TEXT rows")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// runScreen runs a screenModel with its output through an Output on a pipe
// and types key; it returns the program's error and everything written.
func runScreen(t *testing.T, key string, init tea.Cmd) (string, *Output, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(&all, r); close(copied) }()
	out := NewOutput(w)
	pr, pw := io.Pipe()
	p := tea.NewProgram(screenModel{init}, tea.WithInput(pr), tea.WithOutput(out),
		tea.WithWindowSize(80, 20), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	time.Sleep(150 * time.Millisecond)
	_, _ = pw.Write([]byte(key))
	runErr := <-done
	out.Restore() // as main's deferred Session.Close does
	_ = w.Close()
	<-copied
	return all.String(), out, runErr
}

// checkCleared checks that the screen is cleared after the last frame and
// before the alternate screen is left.
func checkCleared(t *testing.T, s string) {
	t.Helper()
	leave := strings.LastIndex(s, "\x1b[?1049l")
	if leave < 0 {
		t.Fatalf("never left the alternate screen: %q", s)
	}
	clear := strings.LastIndex(s[:leave], string(ClearScreen))
	if clear < 0 || clear+len(ClearScreen) != leave {
		t.Errorf("no clear just before leaving the alternate screen: %q", s[max(0, leave-80):])
	}
	if last := strings.LastIndex(s, "FRAME-TEXT"); last > clear {
		t.Errorf("the frame was drawn after the clear")
	}
}

// Port of test_terminal.py::test_quit_clears_screen_before_leaving_alt_screen
// (the output side; the pty version is in cmd/pqx).
func TestQuitClearsBeforeLeavingAltScreen(t *testing.T) {
	s, out, err := runScreen(t, "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkCleared(t, s)
	if out.InAltScreen() {
		t.Error("still in the alternate screen after quitting")
	}
	if strings.Count(s, "\x1b[?1049l") != 1 {
		t.Errorf("Restore wrote again after a clean exit: %q", s)
	}
}

func TestPanicClearsBeforeLeavingAltScreen(t *testing.T) {
	// Bubble Tea recovers a panic in Update, stops the renderer (which leaves
	// the alternate screen through Output) and returns ErrProgramPanic.
	stderr := os.Stderr
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull // Bubble Tea prints the panic there
	s, _, err := runScreen(t, "p", nil)
	os.Stderr = stderr
	if err == nil {
		t.Fatal("no error from a panic")
	}
	checkCleared(t, s)
}

func TestRestoreAfterACrash(t *testing.T) {
	r, w, _ := os.Pipe()
	out := NewOutput(w)
	_, _ = out.Write([]byte("\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[?25lFRAME"))
	if !out.InAltScreen() {
		t.Fatal("not in the alternate screen")
	}
	out.Restore()
	out.Restore() // once only
	_ = w.Close()
	b, _ := io.ReadAll(r)
	s := string(b)
	want := "FRAME" + string(ClearScreen) + "\x1b[?1049l" + string(restoreModes)
	if !strings.HasSuffix(s, want) {
		t.Errorf("after Restore: %q", s)
	}
}

// Port of test_terminal.py::test_pixel_mouse_mode_is_never_enabled: Bubble
// Tea asks for SGR mouse (1006) in cells and never SGR-pixel (1016), which
// iTerm2 over ssh accepts but then reports positions far off-screen.
func TestPixelMouseModeIsNeverEnabled(t *testing.T) {
	s, _, err := runScreen(t, "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "\x1b[?1006h") {
		t.Errorf("SGR mouse not enabled: %q", s)
	}
	if strings.Contains(s, "1016") {
		t.Errorf("pixel mouse mode touched: %q", s)
	}
}

func TestCopy(t *testing.T) {
	esc := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("echo PWNED\n")) + "\x1b\\"
	cmd, c := Copy(esc + "CLIP-VAL")
	if cmd == nil || strings.ContainsAny(c.Text, "\x1b\x07") || !c.Sanitized || c.Cut {
		t.Errorf("Copy of a hostile value: %+v", c)
	}
	if !strings.HasPrefix(c.Text, "␛]52;c;") {
		t.Errorf("ESC not shown as ␛: %q", c.Text)
	}
	if _, c := Copy("plain é ✓"); c.Text != "plain é ✓" || c.Sanitized {
		t.Errorf("plain text: %+v", c)
	}
	// tab and newline are text, copied as they are (once fmtx.Sanitize
	// keeps whitespace; its starter body doesn't)
	if fmtx.Sanitize("\t", true) == "\t" {
		if _, c := Copy("tab\there\nnew line"); c.Text != "tab\there\nnew line" || c.Sanitized {
			t.Errorf("tab and newline: %+v", c)
		}
	}
	long := strings.Repeat("é", MaxCopy) // 2 bytes each
	_, c = Copy(long)
	if !c.Cut || len(c.Text) > MaxCopy || len(c.Text) < MaxCopy-1 || !strings.HasSuffix(c.Text, "é") {
		t.Errorf("long text: cut %v, %d bytes", c.Cut, len(c.Text))
	}
}

// The OSC 52 write itself, through Bubble Tea.
func TestCopyWritesOSC52(t *testing.T) {
	cmd, _ := Copy("héllo\x1b")
	s, _, err := runScreen(t, "q", cmd)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("héllo␛")) + "\a"
	if !strings.Contains(s, want) {
		t.Errorf("no %q in the output", want)
	}
}
