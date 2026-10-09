// Package chrome is pqx's title bar, the Data tab's status line, the key
// bar and the toasts (kit.Chrome, WP10): Python pqx's _render_titlebar,
// _render_status, _render_keys and notify.
package chrome

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Spinner is the busy indicator's frames, one per tick.
const Spinner = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// TickEvery is how often the spinner turns while a task runs.
const TickEvery = 100 * time.Millisecond

// sep separates the title bar's and status line's parts.
const sep = "  ·  "

// Chrome implements kit.Chrome.
type Chrome struct {
	env *kit.Env
	// now is the clock (tests replace it).
	now func() time.Time

	status  kit.StatusMsg
	spin    int
	ticking bool // a tick is on its way

	// countSecs is how long the current view's count took (Python's
	// _count_secs): from its ViewChangedMsg to its TotalMsg.
	viewAt    time.Time
	countSecs float64
	haveSecs  bool

	// hidden is the current column, hidden in the grid, that the status
	// line points out (Python's _hidden_hint).
	hidden string

	toasts []toast
	nextID int
}

// New makes the chrome.
func New(env *kit.Env) *Chrome {
	return &Chrome{env: env, now: time.Now}
}

var _ kit.Chrome = (*Chrome)(nil)

// tickMsg turns the spinner.
type tickMsg struct{}

// expireMsg removes toast id.
type expireMsg struct{ id int }

// Update implements kit.Chrome.
func (c *Chrome) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m := msg.(type) {
	case tickMsg:
		c.ticking = false
		if c.env.Tasks.Busy() {
			c.spin = (c.spin + 1) % len([]rune(Spinner))
		}
	case kit.StatusMsg:
		c.status = m
	case kit.ViewChangedMsg:
		// a new view: the last query's error is gone (Python's apply_filter)
		if c.status.Severity == kit.Error {
			c.status = kit.StatusMsg{}
		}
		c.viewAt = c.now()
		c.haveSecs = false
		c.hidden = ""
	case kit.TotalMsg:
		if c.env.State.Total >= 0 && !c.viewAt.IsZero() {
			c.countSecs = c.now().Sub(c.viewAt).Seconds()
			c.haveSecs = true
		}
	case kit.CursorMsg, kit.ColumnsChangedMsg:
		c.hidden = ""
	case kit.ColumnChangedMsg:
		// another part moved to a column the grid hides: the grid stays put
		// and the status line says so (Python's tab_activated)
		st := c.env.State
		if m.From != "grid" {
			c.hidden = ""
			if st.Hidden[st.Current] {
				c.hidden = st.Current
			}
		}
	case kit.NotifyMsg:
		cmd = c.notify(m)
	case kit.DoneMsg:
		// a task may report with a notice (the export does)
		if n, ok := m.Msg.(kit.NotifyMsg); ok {
			cmd = c.notify(n)
		}
	case expireMsg:
		for i, t := range c.toasts {
			if t.id == m.id {
				c.toasts = append(c.toasts[:i], c.toasts[i+1:]...)
				break
			}
		}
	}
	// the spinner turns while anything runs, and stops ticking when idle
	if c.env.Tasks.Busy() && !c.ticking {
		c.ticking = true
		cmd = tea.Batch(cmd, tea.Tick(TickEvery, func(time.Time) tea.Msg { return tickMsg{} }))
	}
	return cmd
}

// Ticking reports whether the chrome waits for a spinner tick.
func (c *Chrome) Ticking() bool { return c.ticking }

func (c *Chrome) look() kit.Look { return c.env.Look }

func (c *Chrome) dim() styled.Style { return c.look().Style("dim") }

// cut renders t and cuts it to w cells with an ellipsis.
func (c *Chrome) cut(t styled.Text, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(c.look().Render(t), w, "…")
}

// TitleBar implements kit.Chrome: "pqx <version>  ·  file  ·  N rows  ·  C
// columns  ·  size  ·  R row groups", one cell in from each side, cut with an
// ellipsis. The file name comes first: when it doesn't fit after the
// version, the version goes.
func (c *Chrome) TitleBar(w int) string {
	ds := c.env.DS
	width := w - 2
	name := fmtx.Sanitize(baseName(ds.Path()), false)
	version := ""
	if v := c.env.Opts.Version; v != "" && ansi.StringWidth("pqx "+v+sep+name) <= width {
		version = " " + v
	}
	d := c.dim()
	var t styled.Text
	t.Append("pqx", styled.Style{Bold: true})
	t.Append(version, d)
	t.Append(sep, d)
	t.Append(name, styled.Style{Bold: true, Fg: "cyan"})
	t.Append(sep+Commas(ds.NumRows())+" rows"+sep+strconv.Itoa(len(ds.Columns()))+" columns"+sep+
		fmtx.HumanBytes(float64(ds.Info().Size))+sep+Commas(int64(len(ds.RowGroups())))+" row groups", d)
	return " " + c.cut(t, width)
}

// StatusLine implements kit.Chrome, in Python pqx's order: DuckDB can't read
// the file; the last query failed; something runs; no rows; the counts.
func (c *Chrome) StatusLine(w int) string {
	return c.cut(c.statusText(), w)
}

func (c *Chrome) statusText() styled.Text {
	look, d := c.look(), c.dim()
	st, ds, tasks := c.env.State, c.env.DS, c.env.Tasks
	var t styled.Text
	tot := st.Total
	if c.status.Severity == kit.Error && c.status.Text != "" {
		t.Append("✗", look.Style("error"))
		if err := ds.SetupErr(); err != nil {
			t.Append(" DuckDB can't read this file", styled.Style{Bold: true})
			t.Append("   reason: ", d)
			t.Append(SetupReason(err), styled.Style{})
			t.Append("   → Schema (2) and Metadata (5) still work", d)
			return t
		}
		reason, hint, _ := strings.Cut(c.status.Text, "\n")
		if hint == "" {
			hint = "edit with /"
		}
		t.Append(" Query failed", styled.Style{Bold: true})
		t.Append("   reason: ", d)
		t.Append(fmtx.Sanitize(reason, false), styled.Style{})
		t.Append("   → "+fmtx.Sanitize(hint, false)+sep+"previous view kept", d)
		return t
	}
	if list := tasks.List(); len(list) > 0 {
		task := list[0]
		t.Append(string([]rune(Spinner)[c.spin]), look.Style("accent"))
		label := task.Label
		if r := []rune(label); len(r) > 0 {
			label = strings.ToUpper(string(r[0])) + string(r[1:])
		}
		t.Append(fmtx.Sanitize(" "+label, false), styled.Style{}) // (labels can name columns)
		var extra []string
		if tot > 0 {
			extra = append(extra, Commas(tot)+" rows")
		}
		if el := c.now().Sub(task.Started); el >= time.Second {
			s := int(el.Seconds())
			extra = append(extra, pad2(s/60)+":"+pad2(s%60)+" elapsed")
		}
		if len(extra) > 0 {
			t.Append("   "+strings.Join(extra, sep), d)
		}
		return t
	}
	if tot == 0 {
		t.Append("!", look.Style("warning"))
		t.Append(" No matching rows", styled.Style{Bold: true})
		t.Append("   → x clears the filter", d)
		return t
	}
	t.Append("✓", look.Style("success"))
	if tot > 0 {
		t.Append(" "+Commas(tot)+" rows", styled.Style{})
	} else {
		t.Append(" rows", styled.Style{})
	}
	v := st.View
	var bits []string
	if !v.Plain() && tot >= 0 && !v.IsSQL() {
		n := float64(ds.NumRows())
		bits = append(bits, fmtx.Percent(float64(tot), n)+" of "+fmtx.HumanCount(n))
	}
	if v.IsSQL() {
		bits = append(bits, "SQL result")
	}
	if len(v.OrderBy) > 0 {
		arrow := "↑"
		if v.OrderBy[0].Desc {
			arrow = "↓"
		}
		bits = append(bits, "sorted "+fmtx.Sanitize(v.OrderBy[0].Column, false)+" "+arrow)
	}
	if c.haveSecs && !v.Plain() {
		bits = append(bits, strconv.FormatFloat(c.countSecs, 'f', 2, 64)+" s")
	}
	if st.Raw {
		bits = append(bits, "raw values")
	}
	if tot != 0 { // (rows shown, the count known or not: Python's grid.row_count)
		bits = append(bits, "row "+Commas(st.Row))
	}
	if len(bits) > 0 {
		t.Append(sep+strings.Join(bits, sep), d)
	}
	if c.hidden != "" && tot != 0 {
		t.Append("   !", look.Style("warning"))
		t.Append(" "+fmtx.Sanitize(c.hidden, false)+" is hidden", styled.Style{Bold: true})
		t.Append(" · c to show", d)
	}
	if c.status.Text != "" { // a part's own message (not an error)
		icon, role := "✓", "success"
		switch c.status.Severity {
		case kit.Warning:
			icon, role = "!", "warning"
		case kit.Info:
			icon, role = "·", "dim"
		}
		t.Append("   "+icon, look.Style(role))
		t.Append(" "+fmtx.Sanitize(c.status.Text, false), styled.Style{})
	}
	return t
}

// globals are the keys every tab's key bar ends with (Python's KEYS).
var globals = []kit.KeyHint{{Key: "1-5", Help: "tabs"}, {Key: "?", Help: "help"}, {Key: "q", Help: "quit"}}

// KeyBar implements kit.Chrome: the focused part's hints, then any of the
// global ones ("1-5 tabs", "? help", "q quit") not among them, one cell in
// from each side; what doesn't fit is left out by whole words. A part lists the global keys
// itself to place them as Python's KEYS does ("/ filter   x clear filter
// 1-5 tabs   ? help   q quit   s sort …"). Contexts with an "esc" hint (the
// filter, the details pane, a drop-down) get no globals added: Python's
// lists for them have their own.
func (c *Chrome) KeyBar(w int, hints []kit.KeyHint) string {
	all := append([]kit.KeyHint(nil), hints...)
	esc := false
	have := map[string]bool{}
	for _, h := range hints {
		have[h.Key] = true
		esc = esc || h.Key == "esc"
	}
	if !esc {
		for _, g := range globals {
			if !have[g.Key] {
				all = append(all, g)
			}
		}
	}
	d := c.dim()
	var t styled.Text
	for i, h := range all {
		if i > 0 {
			t.Append("   ", styled.Style{})
		}
		t.Append(h.Key, styled.Style{Bold: true})
		t.Append(" "+h.Help, d)
	}
	return " " + c.look().Render(headRunes(t, firstLine(t.Plain, max(0, w-2))))
}

// headRunes is t's first n runes (cells: the key bar's text is all one
// cell wide), with their styles.
func headRunes(t styled.Text, n int) styled.Text {
	r := []rune(t.Plain)
	if n >= len(r) {
		return t
	}
	h := styled.Text{Plain: string(r[:n]), Style: t.Style}
	for _, sp := range t.Spans {
		if sp.Start < n {
			sp.End = min(sp.End, n)
			h.Spans = append(h.Spans, sp)
		}
	}
	return h
}

// firstLine is how many runes of s the first line of s word-wrapped to w
// cells holds, trailing spaces left out: Python's key bar wraps (and shows
// only its first line) rather than cutting. A word longer than the line is
// folded.
func firstLine(s string, w int) int {
	rs := []rune(s)
	pos, lastEnd := 0, 0 // (runes and cells alike: one cell each)
	for i, r := range rs {
		if pos+1 > w {
			if lastEnd == 0 {
				return pos
			}
			return lastEnd
		}
		pos++
		if r != ' ' && (i+1 == len(rs) || rs[i+1] == ' ') {
			lastEnd = pos
		}
	}
	return lastEnd
}

// Commas is n with thousands separators (Python's f"{n:,}").
func Commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

var (
	reErrKind     = regexp.MustCompile(`^[A-Za-z ]+ Error:\s*`)
	reReadFailed  = regexp.MustCompile(`^Failed to read Parquet file '.*?':\s*`)
	reQueryKind   = regexp.MustCompile(`^(Binder|Parser|Catalog|Conversion|Invalid Input|Out of Range) Error:\s*`)
	reUnknownCol  = regexp.MustCompile(`Referenced column ("[^"]+") not found in FROM clause!?`)
	reCandidate   = regexp.MustCompile(`Candidate bindings: "(?:[^".]+\.)?([^"]+)"`)
	statusReasonN = 160
)

// SetupReason is why DuckDB can't read the file, for the status line
// (Python's _show_error for the setup error).
func SetupReason(err error) string {
	first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	first = fmtx.Sanitize(first, false)
	first = reErrKind.ReplaceAllString(first, "")
	first = reReadFailed.ReplaceAllString(first, "")
	return fmtx.Sanitize(cutRunes(first, statusReasonN), false)
}

// QueryError reports a failed query as Python pqx's _show_error does: the
// status line's "✗ Query failed   reason: …   → hint" (a StatusMsg whose
// Text is the reason and, after a newline, the hint: 'did you mean "x"?'
// or none for "edit with /") and a toast with the whole message.
func QueryError(err error) tea.Cmd {
	raw := strings.TrimSpace(err.Error())
	msg := fmtx.Sanitize(raw, true)
	first, _, _ := strings.Cut(raw, "\n")
	first = fmtx.Sanitize(first, false)
	first = reQueryKind.ReplaceAllString(first, "")
	first = reUnknownCol.ReplaceAllString(first, "unknown column $1")
	first = strings.TrimRight(first, "!")
	text := fmtx.Sanitize(cutRunes(first, statusReasonN), false)
	if m := reCandidate.FindStringSubmatch(msg); m != nil {
		text += "\n" + `did you mean "` + m[1] + `"?`
	}
	return tea.Batch(
		kit.Send(kit.StatusMsg{Severity: kit.Error, Text: text}),
		kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed", Text: cutRunes(msg, 600),
			Timeout: 8 * time.Second}))
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
