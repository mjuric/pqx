// Package analysis holds what the Stats and Plot tabs share: the scope of a
// view in words, the sampling toggle ("m"), error notices for their
// queries, and a few text helpers.
package analysis

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/plots"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Spinner is the frame Python pqx shows (static) while profiling or binning.
const Spinner = "⠸"

// Scope is the view in words: "all rows", "SQL result" or "where …"
// (Python's _scope).
func Scope(v data.View) string {
	switch {
	case v.Plain():
		return "all rows"
	case v.IsSQL():
		return "SQL result"
	}
	return "where " + fmtx.Sanitize(v.Where, false)
}

// ToggleSampling is Python's action_toggle_sample: it flips State.Sampling,
// shows a notice and announces the change (the Stats and Plot panes
// recompute on SamplingChangedMsg).
func ToggleSampling(env *kit.Env) tea.Cmd {
	st := env.State
	st.Sampling = !st.Sampling
	text := "✓ Sampling off · stats and plots scan every row"
	if st.Sampling {
		text = "! Sampling on · stats and plots use ~" + fmtx.HumanCount(kit.SampleRows) + " rows"
	}
	return tea.Batch(kit.Send(kit.NotifyMsg{Severity: kit.Info, Text: text, Timeout: 3 * time.Second}),
		kit.Send(kit.SamplingChangedMsg{}))
}

// unreadable remembers, per session, that the "DuckDB can't read this
// file" notice was shown (it is shown once).
var unreadable = map[*kit.State]bool{}

var (
	errPrefix = regexp.MustCompile(`^(Binder|Parser|Catalog|Conversion|Invalid Input|Out of Range) Error:\s*`)
	notFound  = regexp.MustCompile(`Referenced column ("[^"]+") not found in FROM clause!?`)
	setupPfx  = regexp.MustCompile(`^[A-Za-z ]+ Error:\s*`)
	readPfx   = regexp.MustCompile(`^Failed to read Parquet file '.*?':\s*`)
)

// ShowError is Python's _show_error for a stats or plot query: a toast
// with the message and the first line in the status line. Cancellations
// show nothing.
func ShowError(env *kit.Env, err error) tea.Cmd {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	msg := fmtx.Sanitize(strings.TrimSpace(err.Error()), true)
	first, _, _ := strings.Cut(msg, "\n")
	if se := env.DS.SetupErr(); se != nil && errors.Is(err, se) {
		reason := readPfx.ReplaceAllString(setupPfx.ReplaceAllString(first, ""), "")
		cmds := []tea.Cmd{kit.Send(kit.StatusMsg{Severity: kit.Error, Text: cut(reason, 160)})}
		if !unreadable[env.State] {
			unreadable[env.State] = true
			cmds = append(cmds, kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ DuckDB can't read this file",
				Text: cut(msg, 600) + "\n\nSchema and Metadata (from the footer) still work.", Timeout: 12 * time.Second}))
		}
		return tea.Batch(cmds...)
	}
	first = errPrefix.ReplaceAllString(first, "")
	first = notFound.ReplaceAllString(first, "unknown column $1")
	first = strings.TrimRight(first, "!")
	return tea.Batch(
		kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed", Text: cut(msg, 600), Timeout: 8 * time.Second}),
		kit.Send(kit.StatusMsg{Severity: kit.Error, Text: cut(first, 160)}))
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// PlotLook is the theme as plots need it.
func PlotLook(l kit.Look) plots.Look {
	return plots.Look{DarkBG: l.DarkBG(), Accent: l.Style("accent").Fg, Dim: l.Style("dim")}
}

// Commas is n with thousands separators (Python's f"{n:,}").
func Commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// FmtFloat is v with sig significant digits, fixed-point for 1e-3 <= |v| <
// 1e9, else scientific (Python fmt._fmt_float, which fmtx doesn't export).
func FmtFloat(v float64, sig int) string {
	if v == 0 {
		return "0"
	}
	if math.IsNaN(v) {
		return "nan"
	}
	if math.IsInf(v, 0) {
		if v > 0 {
			return "inf"
		}
		return "-inf"
	}
	a := math.Abs(v)
	if a >= 1e-3 && a < 1e9 {
		mag := int(math.Floor(math.Log10(a)))
		dec := max(0, sig-1-mag)
		var s string
		if dec > 0 {
			s = strconv.FormatFloat(v, 'f', dec, 64)
		} else {
			// round(v, sig-1-mag) with a negative number of digits
			p := math.Pow(10, float64(mag+1-sig))
			s = strconv.FormatFloat(math.RoundToEven(v/p)*p, 'f', 0, 64)
		}
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s
	}
	s := strconv.FormatFloat(v, 'e', max(sig-1, 1), 64)
	m, e, _ := strings.Cut(s, "e")
	if strings.Contains(m, ".") {
		m = strings.TrimRight(strings.TrimRight(m, "0"), ".")
	}
	return m + "e" + e
}

// Wrap breaks t into lines at most w cells wide, at spaces where it can
// (Rich's word wrap for a Static), folding words longer than a line.
func Wrap(t styled.Text, w int) []styled.Text {
	if w <= 0 {
		return []styled.Text{t}
	}
	var out []styled.Text
	for _, line := range t.Lines() {
		out = append(out, wrapLine(line, w)...)
	}
	return out
}

func wrapLine(t styled.Text, w int) []styled.Text {
	runes := []rune(t.Plain)
	if ansi.StringWidth(t.Plain) <= w {
		return []styled.Text{t}
	}
	var out []styled.Text
	start := 0
	for start < len(runes) {
		// the longest run of runes from start that fits
		width, end, lastSpace := 0, start, -1
		for end < len(runes) {
			cw := ansi.StringWidth(string(runes[end]))
			if width+cw > w {
				break
			}
			if runes[end] == ' ' {
				lastSpace = end
			}
			width += cw
			end++
		}
		if end >= len(runes) {
			out = append(out, slice(t, start, len(runes)))
			break
		}
		next := end
		if runes[end] == ' ' {
			next = end
		} else if lastSpace > start {
			end, next = lastSpace, lastSpace
		}
		out = append(out, slice(t, start, end))
		for next < len(runes) && runes[next] == ' ' {
			next++
		}
		start = next
	}
	return out
}

// slice is runes [a, b) of t with their styles.
func slice(t styled.Text, a, b int) styled.Text {
	runes := []rune(t.Plain)
	l := styled.Text{Plain: string(runes[a:b]), Style: t.Style, Justify: t.Justify}
	for _, sp := range t.Spans {
		s, e := max(sp.Start, a), min(sp.End, b)
		if s < e {
			l.Spans = append(l.Spans, styled.Span{Start: s - a, End: e - a, Style: sp.Style})
		}
	}
	return l
}
