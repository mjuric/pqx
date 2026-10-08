package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The grid is drawn with plain SGR sequences rather than lipgloss.Style.Render
// per cell: a 300-column screen has thousands of cells and Render is costly.
// Only attributes and the 16-colour palette are used; no background is ever
// painted, so the terminal's own colours show through.
const (
	sgrReset   = "\x1b[m"
	sgrBold    = "\x1b[1m"
	sgrFaint   = "\x1b[2m"
	sgrNormal  = "\x1b[22m" // neither bold nor faint
	sgrReverse = "\x1b[7m"
	sgrNoRev   = "\x1b[27m"
	sgrFgOff   = "\x1b[39m"

	fgRed        = "\x1b[31m"
	fgGreen      = "\x1b[32m"
	fgYellow     = "\x1b[33m"
	fgBlue       = "\x1b[34m"
	fgCyan       = "\x1b[36m"
	fgBrightBlck = "\x1b[90m"
)

func faint(s string) string     { return sgrFaint + s + sgrNormal }
func bold(s string) string      { return sgrBold + s + sgrNormal }
func fg(c, s string) string     { return c + s + sgrFgOff }
func boldFg(c, s string) string { return sgrBold + c + s + sgrFgOff + sgrNormal }

// accent is the focus colour (Python pqx's default --accent blue).
const accent = fgBlue

// unfocusedBorder is the border colour of a panel without focus (Python pqx's
// default --border bright_black).
const unfocusedBorder = fgBrightBlck

// spinner frames, as in Python pqx.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// textWidth is the number of terminal cells s takes. s is plain text (no
// escape sequences); cell text from the data layer is sanitized already.
func textWidth(s string) int {
	if isASCII(s) {
		return len(s)
	}
	return ansi.StringWidth(s)
}

// fit truncates s to at most w cells, ending in "…" when cut.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if isASCII(s) {
		if len(s) <= w {
			return s
		}
		return s[:w-1] + "…"
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// pad writes s (of width sw) and spaces up to w cells; right aligns.
func pad(b *strings.Builder, s string, sw, w int, right bool) {
	if right {
		b.WriteString(spaces(w - sw))
		b.WriteString(s)
		return
	}
	b.WriteString(s)
	b.WriteString(spaces(w - sw))
}

var spaceBuf = strings.Repeat(" ", 512)

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= len(spaceBuf) {
		return spaceBuf[:n]
	}
	return strings.Repeat(" ", n)
}

// commas formats n with thousands separators, as Python's f"{n:,}".
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for d := 0; ; d++ {
		if d > 0 && d%3 == 0 {
			i--
			buf[i] = ','
		}
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
