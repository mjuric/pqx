package chrome

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// DefaultTimeout is how long a notice without a timeout of its own shows
// (Textual's NOTIFICATION_TIMEOUT), whatever its severity.
const DefaultTimeout = 5 * time.Second

type toast struct {
	id  int
	msg kit.NotifyMsg
}

func (c *Chrome) notify(m kit.NotifyMsg) tea.Cmd {
	if m.Title == "" && m.Text == "" {
		return nil
	}
	c.nextID++
	id := c.nextID
	c.toasts = append(c.toasts, toast{id, m})
	return tea.Tick(timeout(m), func(time.Time) tea.Msg { return expireMsg{id} })
}

// timeout is how long a notice shows.
func timeout(m kit.NotifyMsg) time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return DefaultTimeout
}

// Toasts implements kit.Chrome: the notices stacked at the bottom right,
// newest at the bottom, a blank row between them and above the key bar
// (Textual's ToastRack): up to 60 cells wide (half the screen at most), a
// border coloured by severity, the title bold, the text wrapped. Those that
// don't fit the screen's height are left out, oldest first.
//
// While a dialog is open they are laid out as Textual does on the dialog's
// screen, which has no side padding: the rack is 2 cells wider and ends a
// cell further right.
func (c *Chrome) Toasts(w, h int) []kit.Overlay {
	if c.dialogs == 0 {
		return c.toastsIn(c.toasts, w, h, 1)
	}
	return c.toastsIn(c.toasts, w, h, 0)
}

// BehindToasts is what the screen behind the dialogs shows of the notices
// while one is open: Textual keeps there the ones it had when the first
// dialog opened, in its own layout, until each expires. The root draws them
// under the dialogs.
func (c *Chrome) BehindToasts(w, h int) []kit.Overlay {
	if c.dialogs == 0 {
		return nil
	}
	var behind []toast
	for _, t := range c.toasts {
		if c.behind[t.id] {
			behind = append(behind, t)
		}
	}
	return c.toastsIn(behind, w, h, 1)
}

// toastsIn lays out toasts in a screen with pad cells of padding on each
// side.
func (c *Chrome) toastsIn(toasts []toast, w, h, pad int) []kit.Overlay {
	if len(toasts) == 0 || w < 12 || h < 4 {
		return nil
	}
	// Textual's ToastRack: the screen less its side padding and a scroll
	// bar's gutter (2 cells); toasts 60 wide, at most half of that,
	// right-aligned in it
	rack := w - 2*pad - 2
	tw := min(60, rack/2)
	right := w - pad - 2 // the first column right of the rack
	var out []kit.Overlay
	end := h - 1 // the first row below the newest toast: the key bar's
	for i := len(toasts) - 1; i >= 0; i-- {
		box := c.toastBox(toasts[i].msg, tw)
		top := end - (strings.Count(box, "\n") + 1)
		if top < 0 {
			break
		}
		out = append(out, kit.Overlay{X: max(0, right-tw), Y: top, Content: box})
		end = top - 1 // a blank row between toasts
	}
	// drawn oldest first, so a newer one would win where they meet
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (c *Chrome) toastBox(m kit.NotifyMsg, tw int) string {
	look := c.look()
	border := look.Style("border")
	title := styled.Style{Bold: true, Fg: look.Style("success").Fg}
	switch m.Severity {
	case kit.Warning:
		border = look.Style("warning")
		title = styled.Style{Bold: true, Fg: look.Style("warning").Fg}
	case kit.Error:
		border = look.Style("error")
		title = styled.Style{Bold: true, Fg: look.Style("error").Fg}
	}
	inner := tw - 4 // border and padding
	var lines []string
	if m.Title != "" {
		for _, l := range wrap(m.Title, inner) {
			lines = append(lines, look.Render(styled.New(l, title)))
		}
	}
	for _, l := range wrap(m.Text, inner) {
		lines = append(lines, look.Render(styled.New(l, styled.Style{})))
	}
	edge := func(s string) string { return look.Render(styled.New(s, border)) }
	var b strings.Builder
	b.WriteString(edge("┌" + strings.Repeat("─", tw-2) + "┐"))
	for _, l := range lines {
		b.WriteString("\n" + edge("│") + " " + l + strings.Repeat(" ", max(0, inner-ansi.StringWidth(l))) + " " + edge("│"))
	}
	b.WriteString("\n" + edge("└"+strings.Repeat("─", tw-2)+"┘"))
	return b.String()
}

// wrap word-wraps s to w cells as Rich does: a word that doesn't fit
// starts a new line, and one longer than a line is folded; "\n" starts a
// new line.
func wrap(s string, w int) []string {
	if s == "" {
		return nil
	}
	w = max(1, w)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		var line strings.Builder
		lw := 0
		flush := func() {
			out = append(out, strings.TrimRight(line.String(), " "))
			line.Reset()
			lw = 0
		}
		// leading spaces stay (DuckDB's "  ^" under the error)
		body := strings.TrimLeft(para, " ")
		line.WriteString(para[:len(para)-len(body)])
		lw = len(para) - len(body)
		words := strings.Split(body, " ")
		for i, word := range words {
			ww := ansi.StringWidth(word)
			sep := 0
			if i > 0 {
				sep = 1
			}
			if lw > 0 && lw+sep+ww > w {
				flush()
				sep = 0
			}
			if lw == 0 || i == 0 {
				sep = 0
			}
			if sep == 1 {
				line.WriteByte(' ')
				lw++
			}
			for ww > w-lw && ww > 0 { // fold
				cut := ansi.Truncate(word, w-lw, "")
				line.WriteString(cut)
				word = strings.TrimPrefix(word, cut)
				ww = ansi.StringWidth(word)
				flush()
			}
			line.WriteString(word)
			lw += ww
		}
		flush()
	}
	return out
}
